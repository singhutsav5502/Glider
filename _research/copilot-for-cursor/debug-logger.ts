export const logIncomingRequest = (json: any): void => {
    console.log('\n' + '='.repeat(80));
    console.log('📥 INCOMING REQUEST:', new Date().toISOString());
    console.log('📥 ALL KEYS:', Object.keys(json).join(', '));
    console.log('📥 Model:', json.model);
    console.log('📥 Stream:', json.stream);
    console.log('📥 tool_choice (raw):', JSON.stringify(json.tool_choice));
    console.log('📥 # tools:', json.tools?.length ?? 0);
    console.log('📥 # messages:', json.messages?.length ?? 0);
    if (json.input !== undefined) console.log('📥 ⚠️ INPUT FIELD (Responses API):', JSON.stringify(json.input).slice(0, 1000));
    if (json.instructions !== undefined) console.log('📥 ⚠️ INSTRUCTIONS FIELD:', JSON.stringify(json.instructions).slice(0, 500));
    if (json.system) console.log('📥 ⚠️ TOP-LEVEL system FIELD DETECTED:', JSON.stringify(json.system).slice(0, 500));
    if (json.max_tokens) console.log('📥 max_tokens:', json.max_tokens);
    if (json.metadata) console.log('📥 ⚠️ metadata:', JSON.stringify(json.metadata).slice(0, 300));
    if (json.stop_sequences) console.log('📥 ⚠️ stop_sequences:', JSON.stringify(json.stop_sequences));
    if (json.messages) {
        const last3 = json.messages.slice(-3);
        for (const m of last3) {
            const contentPreview = typeof m.content === 'string' 
                ? m.content.slice(0, 200) 
                : JSON.stringify(m.content).slice(0, 500);
            console.log(`📥 MSG [${m.role}]: ${contentPreview}`);
            if (Array.isArray(m.content)) {
                const types = m.content.map((c: any) => c.type).join(', ');
                console.log(`📥   Content types: [${types}]`);
            }
        }
    }
    if (json.tool_choice && typeof json.tool_choice === 'object') {
        console.log('📥 ⚠️ tool_choice is OBJECT:', JSON.stringify(json.tool_choice));
    }
    console.log('='.repeat(80));
};

export const logTransformedRequest = (json: any): void => {
    console.log('\n' + '-'.repeat(80));
    console.log('📤 TRANSFORMED REQUEST:');
    console.log('📤 Model:', json.model);
    console.log('📤 tool_choice (after):', JSON.stringify(json.tool_choice));
    console.log('📤 # tools:', json.tools?.length ?? 0);
    console.log('📤 # messages:', json.messages?.length ?? 0);
    if (json.messages) {
        const last3 = json.messages.slice(-3);
        for (const m of last3) {
            const contentPreview = typeof m.content === 'string'
                ? m.content.slice(0, 200)
                : JSON.stringify(m.content).slice(0, 500);
            console.log(`📤 MSG [${m.role}]: ${contentPreview}`);
            if (m.tool_calls) console.log(`📤   tool_calls: ${JSON.stringify(m.tool_calls).slice(0, 300)}`);
        }
    }
    console.log('-'.repeat(80));
};
