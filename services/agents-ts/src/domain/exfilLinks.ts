const SLASHES = "(?:[a-z][a-z0-9+.-]*:)?[\\\\/]{2}";
const ABSOLUTE = new RegExp(`^${SLASHES}`, "i");
const MARKDOWN_RE = /(!?)\[([^\]]*)\]\(\s*<?([^)\s>]+)>?[^)]*\)/g;
const REFERENCE_RE = /^([ \t]{0,3}\[[^\]]+\]:[ \t]*<?)([^\s>]+)(>?.*)$/gim;
const AUTOLINK_RE = new RegExp(`<(${SLASHES}[^>\\s]+|data:[^>\\s]+)>`, "gi");
const BARE_RE =
  /[a-z][a-z0-9+.-]*:\/\/[^\s)\]>"'<]+|data:[a-z]+\/[a-z0-9.+-]+[;,][^\s)\]>"'<]*/gi;
const HTML_ATTRIBUTE_RE = new RegExp(
  `(\\b(?:src|href|srcset|poster|action|data)\\s*=\\s*["']?\\s*)(${SLASHES}[^"'\\s>]+)`,
  "gi",
);
export const LINK_REMOVED = "[link removed]";

const leavesDepot = (url: string): boolean =>
  /^data:/i.test(url) || ABSOLUTE.test(url);

export function neutraliseLinks(text: string): {
  text: string;
  removed: number;
  urls: string[];
} {
  const urls: string[] = [];
  const strip = (url: string): boolean => {
    const leaves = leavesDepot(url);
    if (leaves) urls.push(url);
    return leaves;
  };
  const cleaned = text
    .replace(MARKDOWN_RE, (whole, bang: string, label: string, url: string) =>
      strip(url) ? (bang === "!" ? "" : label) : whole,
    )
    .replace(REFERENCE_RE, (whole, head: string, url: string, tail: string) =>
      strip(url) ? `${head}${LINK_REMOVED}${tail}` : whole,
    )
    .replace(AUTOLINK_RE, (whole, url: string) =>
      strip(url) ? LINK_REMOVED : whole,
    )
    .replace(HTML_ATTRIBUTE_RE, (whole, prefix: string, url: string) =>
      strip(url) ? `${prefix}${LINK_REMOVED}` : whole,
    )
    .replace(BARE_RE, (url) => (strip(url) ? LINK_REMOVED : url));
  return { text: cleaned, removed: urls.length, urls };
}

export const dataCarryingLinks = (text: string): string[] =>
  neutraliseLinks(text).urls;
