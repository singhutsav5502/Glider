export function needsResponsesAPI(model: string): boolean {
    return /^(?:gpt-5\.(?:[2-9]|\d{2,})(?:-codex)?|o\d+|goldeneye)/i.test(model);
}
