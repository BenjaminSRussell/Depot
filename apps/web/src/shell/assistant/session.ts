const MAX_AGE_MS = 10_000;

let handedOff: { text: string; at: number } | undefined;

export function handOffPrompt(text: string): void {
  handedOff = text.trim() === "" ? undefined : { text, at: Date.now() };
}

export function peekHandedOffPrompt(): string | undefined {
  return handedOff !== undefined && Date.now() - handedOff.at < MAX_AGE_MS ? handedOff.text : undefined;
}

export function clearHandedOffPrompt(): void {
  handedOff = undefined;
}
