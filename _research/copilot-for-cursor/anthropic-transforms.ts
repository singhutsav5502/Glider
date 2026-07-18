const cleanSchema = (schema: any): any => {
    if (!schema || typeof schema !== 'object') return schema;
    if (schema.additionalProperties !== undefined) delete schema.additionalProperties;
    if (schema.$schema !== undefined) delete schema.$schema;
    if (schema.title !== undefined) delete schema.title;
    if (schema.properties) {
        for (const key in schema.properties) cleanSchema(schema.properties[key]);
    }
    if (schema.items) cleanSchema(schema.items);
    return schema;
};

const sanitizeContentPart = (part: any, isClaude: boolean): any | null => {
    if (part.cache_control) delete part.cache_control;

    if (isClaude && (part.type === 'image' || (part.source?.type === 'base64'))) {
        return { type: 'text', text: '[Image Omitted]' };
    }
    if (part.type === 'image' && part.source?.type === 'base64') {
        return { type: 'image_url', image_url: { url: `data:${part.source.media_type};base64,${part.source.data}` } };
    }
    if (part.type === 'image') { part.type = 'image_url'; return part; }

    if (part.type === 'text' || part.type === 'image_url') return part;

    return null;
};

const transformAnthropicFields = (json: any): void => {
    if (json.system) {
        const systemText = typeof json.system === 'string' 
            ? json.system 
            : Array.isArray(json.system) 
                ? json.system.map((s: any) => typeof s === 'string' ? s : s.text || '').join('\n')
                : String(json.system);
        if (json.messages && Array.isArray(json.messages)) {
            const hasSystem = json.messages.some((m: any) => m.role === 'system');
            if (!hasSystem) {
                json.messages.unshift({ role: 'system', content: systemText });
            }
        }
        delete json.system;
        console.log('🔧 Converted top-level system field to system message');
    }

    if (json.stop_sequences) {
        json.stop = json.stop_sequences;
        delete json.stop_sequences;
        console.log('🔧 Converted stop_sequences → stop');
    }

    if (json.max_tokens_to_sample && !json.max_tokens) {
        json.max_tokens = json.max_tokens_to_sample;
        delete json.max_tokens_to_sample;
    }

    const anthropicOnlyFields = ['metadata', 'anthropic_version', 'top_k'];
    for (const field of anthropicOnlyFields) {
        if (json[field] !== undefined) {
            console.log(`🔧 Removing Anthropic-only field: ${field}`);
            delete json[field];
        }
    }
};

const transformTools = (json: any): void => {
    if (!json.tools || !Array.isArray(json.tools)) return;

    json.tools = json.tools.map((tool: any) => {
        let parameters = tool.input_schema || tool.parameters || {};
        parameters = cleanSchema(parameters);
        if (tool.type === 'function' && tool.function) {
            tool.function.parameters = cleanSchema(tool.function.parameters);
            return tool;
        }
        return {
            type: "function",
            function: {
                name: tool.name,
                description: tool.description,
                parameters: parameters 
            }
        };
    });
};

const transformToolChoice = (json: any): void => {
    if (!json.tool_choice || typeof json.tool_choice !== 'object') return;

    if (json.tool_choice.type === 'auto') json.tool_choice = "auto";
    else if (json.tool_choice.type === 'none') json.tool_choice = "none";
    else if (json.tool_choice.type === 'required') json.tool_choice = "required";
    else if (json.tool_choice.type === 'any') json.tool_choice = "required";
    else if (json.tool_choice.type === 'tool' && json.tool_choice.name) {
        json.tool_choice = { type: "function", function: { name: json.tool_choice.name } };
    }
};

const transformMessages = (json: any, isClaude: boolean): void => {
    if (!json.messages || !Array.isArray(json.messages)) return;

    const newMessages: any[] = [];

    for (const msg of json.messages) {
        if (msg.role === 'assistant' && Array.isArray(msg.content)) {
            const textParts: string[] = [];
            const toolCalls: any[] = [];

            for (const part of msg.content) {
                if (part.type === 'tool_use') {
                    toolCalls.push({
                        id: part.id,
                        type: 'function',
                        function: {
                            name: part.name,
                            arguments: typeof part.input === 'string' ? part.input : JSON.stringify(part.input ?? {})
                        }
                    });
                } else if (part.type === 'text') {
                    textParts.push(part.text);
                }
            }

            // Preserve any existing OpenAI-format tool_calls on the message
            // (hybrid format: content is array but tool_calls are separate)
            if (msg.tool_calls && Array.isArray(msg.tool_calls)) {
                for (const tc of msg.tool_calls) {
                    if (!toolCalls.some(t => t.id === tc.id)) {
                        toolCalls.push(tc);
                    }
                }
            }

            const assistantMsg: any = { role: 'assistant' };
            assistantMsg.content = textParts.join('\n') || null;
            if (toolCalls.length > 0) assistantMsg.tool_calls = toolCalls;
            newMessages.push(assistantMsg);
            continue;
        }

        if (msg.role === 'user' && Array.isArray(msg.content)) {
            const toolResults = msg.content.filter((c: any) => c.type === 'tool_result');
            const otherParts = msg.content.filter((c: any) => c.type !== 'tool_result' && c.type !== 'tool_use');

            for (const tr of toolResults) {
                let resultContent = tr.content;
                if (typeof resultContent !== 'string') {
                    if (Array.isArray(resultContent)) {
                        resultContent = resultContent.map((p: any) => {
                            if (p.type === 'text') return p.text || '';
                            // Strip base64 images from tool results — Copilot doesn't
                            // support vision and the base64 blob blows up the token count.
                            if (p.type === 'image' || p.source?.type === 'base64') {
                                return '[Image Omitted]';
                            }
                            return JSON.stringify(p);
                        }).join('\n');
                    } else {
                        resultContent = JSON.stringify(resultContent);
                    }
                }
                newMessages.push({
                    role: 'tool',
                    tool_call_id: tr.tool_use_id,
                    content: resultContent || ''
                });
            }

            if (otherParts.length > 0) {
                const cleaned = otherParts.map((p: any) => sanitizeContentPart(p, isClaude)).filter(Boolean);
                if (cleaned.length > 0) {
                    newMessages.push({ role: 'user', content: cleaned });
                }
            }
            continue;
        }

        if (Array.isArray(msg.content)) {
            const cleaned = msg.content.map((p: any) => sanitizeContentPart(p, isClaude)).filter(Boolean);
            msg.content = cleaned.length > 0 ? cleaned : ' ';
        }
        newMessages.push(msg);
    }

    json.messages = newMessages;

    for (let i = 0; i < json.messages.length; i++) {
        const msg = json.messages[i];
        if (Array.isArray(msg.content) && msg.content.length === 0) {
            msg.content = ' ';
        }
        if (Array.isArray(msg.content) && msg.content.length === 1 && msg.content[0].type === 'text') {
            msg.content = msg.content[0].text || ' ';
        }
    }
};

export const normalizeRequest = (json: any, isClaude: boolean): void => {
    transformAnthropicFields(json);
    transformTools(json);
    transformToolChoice(json);
    transformMessages(json, isClaude);
};
