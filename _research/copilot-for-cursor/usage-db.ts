import { mkdirSync, existsSync, readFileSync } from 'fs';
import { writeFile } from 'fs/promises';
import { homedir } from 'os';
import { join } from 'path';

// ── Types ────────────────────────────────────────────────────────────────────

export interface RequestLog {
    id: number;
    timestamp: number;
    model: string;
    promptTokens: number;
    completionTokens: number;
    totalTokens: number;
    status: number;
    duration: number;
    stream: boolean;
}

interface DailySnapshot {
    date: string;
    requests: number;
    promptTokens: number;
    completionTokens: number;
    totalTokens: number;
    errors: number;
    byModel: Record<string, { requests: number; promptTokens: number; completionTokens: number; totalTokens: number; errors: number }>;
}

interface UsageData {
    version: 1;
    createdAt: number;
    lastSavedAt: number;
    requestIdCounter: number;
    recentRequests: RequestLog[];
    dailySnapshots: DailySnapshot[];
    lifetimeTotals: {
        requests: number;
        promptTokens: number;
        completionTokens: number;
        totalTokens: number;
        errors: number;
    };
}

// ── Config ───────────────────────────────────────────────────────────────────

const MAX_RECENT_REQUESTS = 1000;
const MAX_DAILY_SNAPSHOTS = 90;
const DEBOUNCE_MS = 3000;

const DATA_DIR = process.env.DATA_DIR || join(homedir(), '.copilot-proxy');
const USAGE_FILE = join(DATA_DIR, 'usage.json');

// ── State ────────────────────────────────────────────────────────────────────

let data: UsageData;
let saveTimer: ReturnType<typeof setTimeout> | null = null;
let dirty = false;

// ── Helpers ──────────────────────────────────────────────────────────────────

const todayKey = () => new Date().toISOString().slice(0, 10);

const emptyData = (): UsageData => ({
    version: 1,
    createdAt: Date.now(),
    lastSavedAt: Date.now(),
    requestIdCounter: 0,
    recentRequests: [],
    dailySnapshots: [],
    lifetimeTotals: { requests: 0, promptTokens: 0, completionTokens: 0, totalTokens: 0, errors: 0 },
});

// ── Persistence ──────────────────────────────────────────────────────────────

const loadSync = (): UsageData => {
    try {
        if (!existsSync(USAGE_FILE)) return emptyData();
        const text = readFileSync(USAGE_FILE, 'utf-8');
        return JSON.parse(text) as UsageData;
    } catch {
        return emptyData();
    }
};

const saveToDisk = async () => {
    try {
        data.lastSavedAt = Date.now();
        await writeFile(USAGE_FILE, JSON.stringify(data, null, 2), 'utf-8');
    } catch (e) {
        console.error('Failed to save usage data:', e);
    }
    dirty = false;
};

const scheduleSave = () => {
    dirty = true;
    if (saveTimer) return;
    saveTimer = setTimeout(async () => {
        saveTimer = null;
        if (dirty) await saveToDisk();
    }, DEBOUNCE_MS);
};

// ── Initialize ───────────────────────────────────────────────────────────────

const init = () => {
    if (!existsSync(DATA_DIR)) {
        mkdirSync(DATA_DIR, { recursive: true });
    }
    data = loadSync();
    console.log(`💾 Usage DB: ${USAGE_FILE} (${data.lifetimeTotals.requests} lifetime requests)`);
};

init();

// ── Public API ───────────────────────────────────────────────────────────────

export const getNextRequestId = (): number => {
    data.requestIdCounter++;
    return data.requestIdCounter;
};

export const addRequestLog = (log: RequestLog) => {
    data.recentRequests.push(log);
    if (data.recentRequests.length > MAX_RECENT_REQUESTS) {
        data.recentRequests.shift();
    }

    data.lifetimeTotals.requests++;
    data.lifetimeTotals.promptTokens += log.promptTokens;
    data.lifetimeTotals.completionTokens += log.completionTokens;
    data.lifetimeTotals.totalTokens += log.totalTokens;
    if (log.status >= 400) data.lifetimeTotals.errors++;

    const today = todayKey();
    let snap = data.dailySnapshots.find(s => s.date === today);
    if (!snap) {
        snap = { date: today, requests: 0, promptTokens: 0, completionTokens: 0, totalTokens: 0, errors: 0, byModel: {} };
        data.dailySnapshots.push(snap);
        if (data.dailySnapshots.length > MAX_DAILY_SNAPSHOTS) {
            data.dailySnapshots.shift();
        }
    }
    snap.requests++;
    snap.promptTokens += log.promptTokens;
    snap.completionTokens += log.completionTokens;
    snap.totalTokens += log.totalTokens;
    if (log.status >= 400) snap.errors++;

    if (!snap.byModel[log.model]) {
        snap.byModel[log.model] = { requests: 0, promptTokens: 0, completionTokens: 0, totalTokens: 0, errors: 0 };
    }
    const m = snap.byModel[log.model];
    m.requests++;
    m.promptTokens += log.promptTokens;
    m.completionTokens += log.completionTokens;
    m.totalTokens += log.totalTokens;
    if (log.status >= 400) m.errors++;

    scheduleSave();
};

export const getRecentRequests = (): RequestLog[] => data.recentRequests;

export const getUsageStats = () => {
    const logs = data.recentRequests;
    const byModel = Object.entries(
        logs.reduce((acc, r) => {
            if (!acc[r.model]) acc[r.model] = { requests: 0, promptTokens: 0, completionTokens: 0, totalTokens: 0, errors: 0, totalDuration: 0, avgDuration: 0 };
            acc[r.model].requests++;
            acc[r.model].promptTokens += r.promptTokens;
            acc[r.model].completionTokens += r.completionTokens;
            acc[r.model].totalTokens += r.totalTokens;
            if (r.status >= 400) acc[r.model].errors++;
            acc[r.model].totalDuration += r.duration;
            acc[r.model].avgDuration = Math.round(acc[r.model].totalDuration / acc[r.model].requests);
            return acc;
        }, {} as Record<string, any>),
    ).map(([model, d]) => ({ model, ...d }));

    return {
        totalRequests: data.lifetimeTotals.requests,
        totalPromptTokens: data.lifetimeTotals.promptTokens,
        totalCompletionTokens: data.lifetimeTotals.completionTokens,
        totalTokens: data.lifetimeTotals.totalTokens,
        totalErrors: data.lifetimeTotals.errors,
        byModel,
        recentRequests: logs.slice(-50).reverse(),
        dailySnapshots: data.dailySnapshots,
        persistence: {
            dataDir: DATA_DIR,
            file: USAGE_FILE,
            lastSavedAt: data.lastSavedAt,
            createdAt: data.createdAt,
        },
    };
};

export const flushToDisk = async () => {
    if (saveTimer) {
        clearTimeout(saveTimer);
        saveTimer = null;
    }
    await saveToDisk();
};

process.on('beforeExit', async () => { await flushToDisk(); });
process.on('SIGINT', async () => { await flushToDisk(); process.exit(0); });
process.on('SIGTERM', async () => { await flushToDisk(); process.exit(0); });
