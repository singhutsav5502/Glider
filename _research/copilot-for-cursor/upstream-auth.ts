import { existsSync, readFileSync, writeFileSync, mkdirSync } from 'fs';
import { homedir } from 'os';
import { join } from 'path';
import { randomBytes } from 'crypto';

const DEFAULT_DATA_DIR = join(homedir(), '.local', 'share', 'copilot-api');

interface CopilotApiConfig {
    auth?: { apiKeys?: string[] };
    [key: string]: unknown;
}

let cachedKey: string | null = null;

function getConfigPath(): string {
    const dataDir = process.env.COPILOT_API_HOME || DEFAULT_DATA_DIR;
    return join(dataDir, 'config.json');
}

function loadUpstreamConfig(): CopilotApiConfig {
    const configPath = getConfigPath();
    try {
        if (existsSync(configPath)) {
            return JSON.parse(readFileSync(configPath, 'utf-8'));
        }
    } catch {}
    return {};
}

function saveUpstreamConfig(config: CopilotApiConfig): void {
    const configPath = getConfigPath();
    const dir = join(configPath, '..');
    if (!existsSync(dir)) mkdirSync(dir, { recursive: true });
    writeFileSync(configPath, JSON.stringify(config, null, 2), 'utf-8');
    cachedKey = null;
}

export function getUpstreamApiKeys(): string[] {
    const config = loadUpstreamConfig();
    return Array.isArray(config.auth?.apiKeys) ? config.auth!.apiKeys! : [];
}

export function getUpstreamApiKey(): string {
    if (cachedKey) return cachedKey;

    const keys = getUpstreamApiKeys();
    if (keys.length > 0) {
        cachedKey = keys[0];
        return cachedKey;
    }

    cachedKey = 'dummy';
    return cachedKey;
}

export function getUpstreamAuthHeader(): string {
    return `Bearer ${getUpstreamApiKey()}`;
}

export function createUpstreamApiKey(): string {
    const config = loadUpstreamConfig();
    if (!config.auth) config.auth = {};
    if (!Array.isArray(config.auth.apiKeys)) config.auth.apiKeys = [];

    const newKey = 'sk-copilot-' + randomBytes(16).toString('base64url');
    config.auth.apiKeys.push(newKey);
    saveUpstreamConfig(config);
    return newKey;
}

export function deleteUpstreamApiKey(key: string): boolean {
    const config = loadUpstreamConfig();
    const keys = config.auth?.apiKeys;
    if (!Array.isArray(keys)) return false;

    const idx = keys.indexOf(key);
    if (idx === -1) return false;

    keys.splice(idx, 1);
    saveUpstreamConfig(config);
    return true;
}
