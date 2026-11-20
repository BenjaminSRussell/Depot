const ABSOLUTE = /^(?:https?:)?\/\//i;
const MARKDOWN_RE = /(!?)\[([^\]]*)\]\(\s*<?([^)\s>]+)>?[^)]*\)/g;
const AUTOLINK_RE = /<((?:https?:)?\/\/[^>\s]+|data:[^>\s]+)>/gi;
const BARE_RE = /https?:\/\/[^\s)\]>"'<]+|data:[a-z]+\/[a-z0-9.+-]+[;,][^\s)\]>"'<]*/gi;
const HTML_ATTRIBUTE_RE = /(\b(?:src|href|srcset|poster|action|data)\s*=\s*["']?\s*)((?:https?:)?\/\/[^"'\s>]+)/gi;
export const LINK_REMOVED = "[link removed]";

const leavesDepot = (url: string): boolean => /^data:/i.test(url) || ABSOLUTE.test(url);

export function neutraliseLinks(text: string): { text: string; removed: number; urls: string[] } {
  const urls: string[] = [];
  const strip = (url: string): boolean => {
    const leaves = leavesDepot(url);
    if (leaves) urls.push(url);
    return leaves;
  };
  const cleaned = text
    .replace(MARKDOWN_RE, (whole, bang: string, label: string, url: string) => (strip(url) ? (bang === "!" ? "" : label) : whole))
    .replace(AUTOLINK_RE, (whole, url: string) => (strip(url) ? LINK_REMOVED : whole))
    .replace(HTML_ATTRIBUTE_RE, (whole, prefix: string, url: string) => (strip(url) ? `${prefix}${LINK_REMOVED}` : whole))
    .replace(BARE_RE, (url) => (strip(url) ? LINK_REMOVED : url));
  return { text: cleaned, removed: urls.length, urls };
}

export const dataCarryingLinks = (text: string): string[] => neutraliseLinks(text).urls;
