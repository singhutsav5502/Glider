import { getUpstreamAuthHeader } from './upstream-auth';
import { needsResponsesAPI } from './model-routing';

// ── Global config ─────────────────────────────────────────────────────────────
let maxModeEnabled = false;

export function enableMaxMode(): void {
    maxModeEnabled = true;
}

export function isMaxMode(): boolean {
    return maxModeEnabled;
}

// ── Model token limits cache ──────────────────────────────────────────────────
interface ModelLimits {
    maxInputTokens: number;
    maxOutputTokens: number;
}

const modelLimitsCache = new Map<string, ModelLimits>();

// Fallback defaults — only used when upstream /v1/models doesn't return capabilities.limits.
// Real limits are fetched dynamically from the copilot-api at startup via fetchAndCacheModelLimits().
// Output token values: Claude 64K (Sonnet 3.5/4 extended), GPT-4/5 16K, o1/o3 100K reasoning.
const DEFAULT_LIMITS: Record<string, ModelLimits> = {
    'claude': { maxInputTokens: 200000, maxOutputTokens: 64000 },
    'gpt-4': { maxInputTokens: 128000, maxOutputTokens: 16384 },
    'gpt-5': { maxInputTokens: 128000, maxOutputTokens: 16384 },
    'o1': { maxInputTokens: 200000, maxOutputTokens: 100000 },
    'o3': { maxInputTokens: 200000, maxOutputTokens: 100000 },
    'default': { maxInputTokens: 128000, maxOutputTokens: 16384 }, // conservative general-purpose fallback
};

function getDefaultLimits(model: string): ModelLimits {
    const lower = model.toLowerCase();
    for (const [prefix, limits] of Object.entries(DEFAULT_LIMITS)) {
        if (prefix !== 'default' && lower.includes(prefix)) return limits;
    }
    return DEFAULT_LIMITS['default'];
}

export async function fetchAndCacheModelLimits(targetUrl: string): Promise<void> {
    try {
        const resp = await fetch(new URL('/v1/models', targetUrl).toString(), {
            headers: { 'Authorization': getUpstreamAuthHeader() },
            signal: AbortSignal.timeout(10000),
        });
        if (!resp.ok) return;
        const data = await resp.json() as any;
        if (!data.data || !Array.isArray(data.data)) return;

        for (const model of data.data) {
            const limits = model.capabilities?.limits;
            if (limits) {
                modelLimitsCache.set(model.id, {
                    maxInputTokens: limits.max_prompt_tokens || limits.max_input_tokens || getDefaultLimits(model.id).maxInputTokens,
                    maxOutputTokens: limits.max_output_tokens || getDefaultLimits(model.id).maxOutputTokens,
                });
            }
        }
        console.log(`📋 Max mode: cached token limits for ${modelLimitsCache.size} models`);
        for (const [id, lim] of modelLimitsCache) {
            console.log(`   ${id}: input=${lim.maxInputTokens}, output=${lim.maxOutputTokens}`);
        }
    } catch (e: any) {
        console.warn(`⚠️ Max mode: failed to fetch model limits: ${e?.message || e}`);
    }
}

export function getModelLimits(model: string): ModelLimits {
    return modelLimitsCache.get(model) || getDefaultLimits(model);
}

// ── Token estimation ──────────────────────────────────────────────────────────
// Simple char/4 heuristic — fast, zero-dependency, ~80% accurate for English.
// For mixed CJK content each character ≈ 1-2 tokens, so we use a blended ratio.

function estimateTokens(text: string): number {
    if (!text) return 0;
    // rough estimate: ascii chars / 4, non-ascii chars / 1.5
    let ascii = 0, nonAscii = 0;
    for (let i = 0; i < text.length; i++) {
        if (text.charCodeAt(i) < 128) ascii++;
        else nonAscii++;
    }
    return Math.ceil(ascii / 4 + nonAscii / 1.5);
}

function estimateMessagesTokens(messages: any[]): number {
    let total = 0;
    for (const msg of messages) {
        // role overhead
        total += 4;
        if (typeof msg.content === 'string') {
            total += estimateTokens(msg.content);
        } else if (Array.isArray(msg.content)) {
            for (const part of msg.content) {
                if (part.type === 'text') total += estimateTokens(part.text || '');
                else total += estimateTokens(JSON.stringify(part));
            }
        }
        // tool calls overhead
        if (msg.tool_calls) {
            total += estimateTokens(JSON.stringify(msg.tool_calls));
        }
    }
    return total;
}

// ── Helpers ───────────────────────────────────────────────────────────────────
function truncateContent(content: string, maxChars: number): string {
    if (content.length <= maxChars) return content;
    return content.slice(0, maxChars) + '\n... [truncated]';
}

function extractResponsesTextContent(data: any): string {
    const outputMessages = (data.output || []).filter((item: any) =>
        item.type === 'message' && Array.isArray(item.content)
    );
    const textParts = outputMessages
        .flatMap((item: any) => item.content)
        .filter((part: any) => part.type === 'output_text');
    if (textParts.length === 0) {
        console.warn('⚠️ Max mode: Responses summarization returned no output_text parts');
    }
    return textParts.map((part: any) => part.text).join('');
}

// ── Summarization prompt ──────────────────────────────────────────────────────
// Inspired by claude-code/opencode compaction prompts, adapted for proxy use.
const SUMMARIZATION_PROMPT = `Your task is to create a detailed summary of the conversation so far, paying close attention to the user's explicit requests and the assistant's previous actions.

Analyze each message chronologically and identify:
- The user's primary goals and requests
- Key technical concepts and decisions
- Files and code sections discussed or modified
- Problems encountered and solutions applied
- The current state of work in progress

Your summary MUST:
1. Preserve all file paths, function names, variable names, and code snippets mentioned
2. Retain exact error messages and their resolutions
3. Capture the user's original intent and any refinements
4. Note what has been completed vs what remains to be done
5. Include enough technical detail to continue the conversation seamlessly

Format as a structured summary, not a conversation replay. Be concise but do NOT omit any technical details that would be needed to continue the work.`;

// ── Compaction logic ──────────────────────────────────────────────────────────
// Soft threshold (--max mode): proactively compact at 80% so long sessions stay smooth.
const COMPACT_THRESHOLD_SOFT = 0.80;
// Hard threshold (always on): safety net — compact only when we're about to overflow.
// Keeps non-max users from ever hitting upstream context-length errors.
const COMPACT_THRESHOLD_HARD = 0.95;
// Keep the most recent N messages untouched to preserve immediate context
const KEEP_RECENT_MESSAGES = 10;
// Never compact if total messages are below this count
const MIN_MESSAGES_FOR_COMPACTION = 15;
// Minimum old messages worth summarizing (below this, compaction is skipped)
const MIN_MESSAGES_TO_SUMMARIZE = 3;
// Max characters per individual message when building the summarization input
const MAX_MESSAGE_CHARS_FOR_SUMMARY = 8000;
// Acknowledgment message inserted after the summary to maintain conversation flow
const SUMMARY_ACKNOWLEDGMENT = 'Understood. I have the full context from the conversation summary. Let me continue.';

export async function compactIfNeeded(
    json: any,
    targetModel: string,
    targetUrl: string,
): Promise<any> {
    if (!json.messages || !Array.isArray(json.messages) || json.messages.length < MIN_MESSAGES_FOR_COMPACTION) {
        return json;
    }

    const limits = getModelLimits(targetModel);
    const estimated = estimateMessagesTokens(json.messages);
    // --max → aggressive (soft) compaction at 80%; otherwise act only as a safety net at 95%.
    const thresholdFraction = maxModeEnabled ? COMPACT_THRESHOLD_SOFT : COMPACT_THRESHOLD_HARD;
    const threshold = Math.floor(limits.maxInputTokens * thresholdFraction);

    if (estimated <= threshold) {
        return json;
    }

    const mode = maxModeEnabled ? 'soft' : 'hard';
    console.log(`🗜️ ${mode === 'soft' ? 'Max mode' : 'Safety net'}: estimated ${estimated} tokens exceeds ${thresholdFraction * 100}% of ${limits.maxInputTokens} — compacting`);

    // Split: system messages + old messages to summarize + recent messages to keep
    const systemMsgs = json.messages.filter((m: any) => m.role === 'system');
    const nonSystemMsgs = json.messages.filter((m: any) => m.role !== 'system');
    // Keep at most half of non-system messages to ensure there's enough old content to summarize
    const keepCount = Math.min(KEEP_RECENT_MESSAGES, Math.floor(nonSystemMsgs.length / 2));
    const recentMsgs = nonSystemMsgs.slice(-keepCount);
    const oldMsgs = nonSystemMsgs.slice(0, -keepCount);

    if (oldMsgs.length < MIN_MESSAGES_TO_SUMMARIZE) {
        // Not enough old content to summarize meaningfully — but if we're over the hard
        // ceiling we still need to do *something*, so drop oldest non-system messages.
        return hardTruncateIfOver(json, systemMsgs, nonSystemMsgs, limits.maxInputTokens);
    }

    try {
        const summary = await callSummarize(targetModel, oldMsgs, targetUrl);
        if (!summary) {
            // Summarization failed — fall back to hard truncation so we don't 500.
            return hardTruncateIfOver(json, systemMsgs, nonSystemMsgs, limits.maxInputTokens);
        }

        console.log(`🗜️ Compacted ${oldMsgs.length} messages → 1 summary (${estimateTokens(summary)} est. tokens)`);

        // Rebuild messages: system + summary-as-user-message + recent
        json.messages = [
            ...systemMsgs,
            { role: 'user', content: `[Conversation Summary]\n${summary}` },
            { role: 'assistant', content: SUMMARY_ACKNOWLEDGMENT },
            ...recentMsgs,
        ];

        // Even after summarization the prompt may still be over budget (e.g. huge recent
        // messages). Apply hard truncation as the final safety net.
        const postEst = estimateMessagesTokens(json.messages);
        const hardCeiling = Math.floor(limits.maxInputTokens * COMPACT_THRESHOLD_HARD);
        if (postEst > hardCeiling) {
            json.messages = hardTruncateMessages(json.messages, hardCeiling);
        }

        return json;
    } catch (e: any) {
        console.error(`❌ Compaction failed, falling back to hard truncation:`, e?.message || e);
        return hardTruncateIfOver(json, systemMsgs, nonSystemMsgs, limits.maxInputTokens);
    }
}

// ── Hard truncation (last-resort safety net) ──────────────────────────────────
// Drops oldest non-system messages until estimated tokens are under the hard ceiling.
// Zero LLM calls — used when summarization fails or there's nothing worth summarizing.
function hardTruncateMessages(messages: any[], targetTokens: number): any[] {
    const system = messages.filter((m: any) => m.role === 'system');
    let rest = messages.filter((m: any) => m.role !== 'system');
    let dropped = 0;
    while (rest.length > 2 && estimateMessagesTokens([...system, ...rest]) > targetTokens) {
        rest.shift();
        dropped++;
    }
    if (dropped > 0) {
        console.log(`🗜️ Hard truncation: dropped ${dropped} oldest message(s) to fit token budget`);
    }
    return [...system, ...rest];
}

function hardTruncateIfOver(
    json: any,
    systemMsgs: any[],
    nonSystemMsgs: any[],
    maxInputTokens: number,
): any {
    const ceiling = Math.floor(maxInputTokens * COMPACT_THRESHOLD_HARD);
    const est = estimateMessagesTokens([...systemMsgs, ...nonSystemMsgs]);
    if (est <= ceiling) return json;
    json.messages = hardTruncateMessages([...systemMsgs, ...nonSystemMsgs], ceiling);
    return json;
}

async function callSummarize(model: string, messages: any[], targetUrl: string): Promise<string | null> {
    const conversationText = messages.map(m => {
        const content = typeof m.content === 'string'
            ? m.content
            : Array.isArray(m.content)
                ? m.content.map((p: any) => p.text || JSON.stringify(p)).join('\n')
                : JSON.stringify(m.content);
        const role = m.role || 'unknown';
        const truncated = truncateContent(content, MAX_MESSAGE_CHARS_FOR_SUMMARY);
        return `[${role}]: ${truncated}`;
    }).join('\n\n');

    console.log(`🗜️ Max mode: sending summarization request (${messages.length} messages → ${model})`);

    if (needsResponsesAPI(model)) {
        const responsesUrl = new URL('/v1/responses', targetUrl);
        const responsesBody = JSON.stringify({
            model,
            instructions: SUMMARIZATION_PROMPT,
            input: `Please summarize the following conversation:\n\n${conversationText}`,
            max_output_tokens: 4096,
            temperature: 0.2,
            stream: false,
        });

        const resp = await fetch(responsesUrl.toString(), {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
                'Authorization': getUpstreamAuthHeader(),
            },
            body: responsesBody,
        });

        if (!resp.ok) {
            const errText = await resp.text();
            console.error(`❌ Max mode summarization failed (${resp.status}):`, errText.slice(0, 500));
            return null;
        }

        const data = await resp.json() as any;
        const content = extractResponsesTextContent(data);

        if (content) {
            console.log(`🗜️ Max mode: summarization complete (${estimateTokens(content)} est. tokens)`);
        }

        return content || null;
    }

    const summarizeMessages = [
        { role: 'system', content: SUMMARIZATION_PROMPT },
        {
            role: 'user',
            content: `Please summarize the following conversation:\n\n${conversationText}`,
        },
    ];

    const chatBody = JSON.stringify({
        model,
        messages: summarizeMessages,
        max_tokens: 4096,
        temperature: 0.2,
        stream: false,
    });

    const chatUrl = new URL('/v1/chat/completions', targetUrl);
    const resp = await fetch(chatUrl.toString(), {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            'Authorization': getUpstreamAuthHeader(),
        },
        body: chatBody,
    });

    if (!resp.ok) {
        const errText = await resp.text();
        console.error(`❌ Max mode summarization failed (${resp.status}):`, errText.slice(0, 500));
        return null;
    }

    const data = await resp.json() as any;
    const content = data.choices?.[0]?.message?.content;

    if (content) {
        console.log(`🗜️ Max mode: summarization complete (${estimateTokens(content)} est. tokens)`);
    }

    return content || null;
}
