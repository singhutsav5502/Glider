import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'fs';
import { homedir } from 'os';
import { join } from 'path';

export interface ApiKey {
    id: string;
    name: string;
    key: string;
    active: boolean;
    createdAt: number;
}

export interface AuthConfig {
    requireApiKey: boolean;
    keys: ApiKey[];
}

const CONFIG_DIR = join(homedir(), '.copilot-proxy');
const CONFIG_PATH = join(CONFIG_DIR, 'auth.json');

const DEFAULT_CONFIG: AuthConfig = { requireApiKey: false, keys: [] };

let cachedConfig: AuthConfig | null = null;

export function loadAuthConfig(): AuthConfig {
    if (cachedConfig) return cachedConfig;
    try {
        if (!existsSync(CONFIG_PATH)) return { ...DEFAULT_CONFIG, keys: [] };
        const raw = readFileSync(CONFIG_PATH, 'utf-8');
        const parsed = JSON.parse(raw);
        cachedConfig = {
            requireApiKey: !!parsed.requireApiKey,
            keys: Array.isArray(parsed.keys) ? parsed.keys : [],
        };
        return cachedConfig;
    } catch {
        return { ...DEFAULT_CONFIG, keys: [] };
    }
}

export function saveAuthConfig(config: AuthConfig): void {
    if (!existsSync(CONFIG_DIR)) mkdirSync(CONFIG_DIR, { recursive: true });
    writeFileSync(CONFIG_PATH, JSON.stringify(config, null, 2), 'utf-8');
    cachedConfig = null; // Invalidate cache on write
}

function randomHex(bytes: number): string {
    const arr = new Uint8Array(bytes);
    crypto.getRandomValues(arr);
    return Array.from(arr, b => b.toString(16).padStart(2, '0')).join('');
}

export function generateApiKey(name: string): ApiKey {
    return {
        id: randomHex(4),            // 8-char hex
        name,
        key: 'cpk-' + randomHex(16), // cpk- + 32 hex chars
        active: true,
        createdAt: Date.now(),
    };
}

export function validateApiKey(key: string): boolean {
    const config = loadAuthConfig();
    return config.keys.some(k => k.key === key && k.active);
}
