import { describe, expect, it } from "vitest";
import { isAllowedDestination, neutraliseLinks, unsafeTail } from "../src/domain/exfilLinks.js";

const clean = (text: string): string => neutraliseLinks(text).text;

describe("neutraliseLinks", () => {
  it.each([
    ["a markdown link keeps its label", "a [label](https://evil.example/c) b", "a label b"],
    ["host-label data", "a [x](https://c2VjcmV0.evil.example/) b", "a x b"],
    ["userinfo data", "a [x](https://c2VjcmV0@evil.example/) b", "a x b"],
    ["an image to a relative path", "a ![alt](/purchasing/x/1) b", "a  b"],
    ["an image with an absolute url", "a ![alt](https://evil.example/i.png) b", "a  b"],
    ["protocol-relative", "[x](//evil.example/a)", "x"],
    ["a backslash trick", "[x](/\\evil.example/a)", "x"],
    ["a javascript uri", "[x](javascript:alert(1))", "x"],
    ["an angle destination with spaces", "[x](<https://evil.example/a b>)", "x"],
    ["a destination with a tab smuggled slash", "[x](/\t/evil.example)", "x"],
    ["an entity smuggled slash", "[x](/&#47;evil.example)", "x"],
    ["a nested bracket label", "[a [b](https://evil.example/1)](/ok/1)", "[a b](/ok/1)"],
    ["a title after the destination", '[x](https://evil.example/a "t")', "x"],
    ["a reference definition", "see [x][1]\n\n[1]: https://evil.example/c?d=1\nnext", "see [x][1]\n\n\nnext"],
    ["a protocol-relative reference definition", "[1]: //evil.example/c\nnext", "\nnext"],
    ["a raw html img", 'a <img src="https://evil.example/i.png"> b', "a  b"],
    ["a raw html img with an angle in an attribute", 'a <IMG alt="x" src=//evil.example/i> b', "a  b"],
    ["a raw html anchor keeps its text", 'a <a href="https://evil.example/c">here</a> b', "a here b"],
    ["an iframe", '<iframe src="//evil.example"></iframe>', ""],
    ["an autolink", "a <https://evil.example/c?d=1> b", "a [link removed] b"],
    ["a bare url", "a https://evil.example/c?d=1 b", "a [link removed] b"],
    ["a www host", "a www.evil.example/x b", "a [link removed] b"],
    ["a data uri", "a data:text/html;base64,PGgxPg== b", "a [link removed] b"],
    ["a vbscript uri", "a vbscript:msgbox b", "a [link removed] b"],
    ["a file uri", "a file:///etc/passwd b", "a [link removed] b"],
    ["a relative record link", "[PO](/purchasing/purchase-orders/123)", "[PO](/purchasing/purchase-orders/123)"],
    ["a citation marker and pseudonym tokens", "[[cite:INV-7]] [PAY-1]: [EMAIL-1]", "[[cite:INV-7]] [PAY-1]: [EMAIL-1]"],
    ["adjacent citations", "[[cite:A]][[cite:B]]", "[[cite:A]][[cite:B]]"],
    ["plain text with a colon", "Note: the metadata:value is 1.", "Note: the metadata:value is 1."],
  ])("%s", (_name, text, expected) => {
    expect(clean(text)).toBe(expected);
  });

  it("counts removals", () => {
    expect(neutraliseLinks("[a](//e.example) ![b](/x/1) https://e.example").removed).toBe(3);
    expect(neutraliseLinks("[PO](/purchasing/purchase-orders/123)").removed).toBe(0);
  });

  it("allows only a single-slash relative path", () => {
    expect(["/a", "/purchasing/purchase-orders/123"].every(isAllowedDestination)).toBe(true);
    expect(["/", "//a", "/\\a", "a/b", "https://a", "/a//b", "/a b", "/a&b"].some(isAllowedDestination)).toBe(false);
  });

  it("stays linear on pathological input", () => {
    const started = performance.now();
    for (const unit of ["[](", "](", "[[", "<a", "![", "http://", "[x](<"]) neutraliseLinks(unit.repeat(40_000));
    expect(performance.now() - started).toBeLessThan(5000);
  });
});

describe("unsafeTail", () => {
  it.each([
    ["see [la", 4],
    ["see [label]", 4],
    ["see [label](/x/1", 4],
    ["see ![alt", 4],
    ["see <img src=", 4],
    ["wow!", 3],
  ])("holds the open construct in %j", (text, hold) => {
    expect(unsafeTail(text)).toBe(hold);
  });

  it("holds nothing once constructs are closed", () => {
    const closed = "see [label](/x/1) and <b> done. [PAY-1] ok";
    expect(unsafeTail(closed)).toBe(closed.length);
  });
});
