import { useState, type ReactNode } from "react";
import guide from "./docs/user-guide.md?raw";
import testingBasics from "./docs/testing-basics.md?raw";

// A deliberately small Markdown renderer for the bundled documents:
// headings, paragraphs, (nested) lists, tables, **bold** and `code`.
// Everything is rendered as React text nodes, so no HTML is injected.
function inline(text: string): ReactNode[] {
  return text
    .split(/(\*\*[^*]+\*\*|`[^`]+`)/g)
    .map((part, i) =>
      part.startsWith("**") && part.endsWith("**") && part.length > 4 ? (
        <strong key={i}>{part.slice(2, -2)}</strong>
      ) : part.startsWith("`") && part.endsWith("`") && part.length > 2 ? (
        <code key={i}>{part.slice(1, -1)}</code>
      ) : (
        part
      ),
    );
}
const slug = (s: string) => s.replace(/\s+/g, "-");
type Item = { text: string; children: string[] };
function list(lines: string[], key: number): ReactNode {
  const ordered = /^\d+\.\s/.test(lines[0].trim());
  const items: Item[] = [];
  for (const line of lines) {
    const nested = /^\s{2,}/.test(line);
    const text = line.trim().replace(/^(-|\d+\.)\s+/, "");
    if (nested && items.length)
      items[items.length - 1].children.push(line.slice(2));
    else items.push({ text, children: [] });
  }
  const body = items.map((it, i) => (
    <li key={i}>
      {inline(it.text)}
      {it.children.length > 0 && list(it.children, 0)}
    </li>
  ));
  return ordered ? <ol key={key}>{body}</ol> : <ul key={key}>{body}</ul>;
}
function render(md: string) {
  const lines = md.split("\n");
  const out: ReactNode[] = [];
  const toc: { id: string; text: string }[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim()) {
      i++;
    } else if (line.startsWith("#")) {
      const level = line.match(/^#+/)![0].length;
      const text = line.slice(level).trim();
      if (level === 1) out.push(<h1 key={i}>{text}</h1>);
      else if (level === 2) {
        const id = "doc-" + slug(text);
        toc.push({ id, text });
        out.push(
          <h2 key={i} id={id}>
            {text}
          </h2>,
        );
      } else out.push(<h3 key={i}>{text}</h3>);
      i++;
    } else if (line.startsWith("|")) {
      const rows: string[][] = [];
      while (i < lines.length && lines[i].startsWith("|")) {
        if (!/^\|[\s|:-]+\|$/.test(lines[i].trim()))
          rows.push(
            lines[i]
              .trim()
              .slice(1, -1)
              .split("|")
              .map((c) => c.trim()),
          );
        i++;
      }
      const [head, ...body] = rows;
      out.push(
        <div className="table-wrap" key={i}>
          <table>
            <thead>
              <tr>
                {head.map((c, j) => (
                  <th key={j}>{inline(c)}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {body.map((r, k) => (
                <tr key={k}>
                  {r.map((c, j) => (
                    <td key={j}>{inline(c)}</td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>,
      );
    } else if (/^(-|\d+\.)\s/.test(line)) {
      const block: string[] = [];
      while (i < lines.length && /^(\s*-|\s*\d+\.)\s/.test(lines[i]))
        block.push(lines[i++]);
      out.push(list(block, i));
    } else {
      const para: string[] = [];
      while (
        i < lines.length &&
        lines[i].trim() &&
        !/^(#|\||-\s|\d+\.\s)/.test(lines[i])
      )
        para.push(lines[i++]);
      out.push(
        <p key={i}>
          {para.map((p, j) => (
            <span key={j}>
              {j > 0 && <br />}
              {inline(p)}
            </span>
          ))}
        </p>,
      );
    }
  }
  return { out, toc };
}
const pages = [
  { id: "guide", title: "利用ガイド", md: guide },
  { id: "testing", title: "テストの基礎知識", md: testingBasics },
].map((p) => ({ ...p, ...render(p.md) }));
export function Docs() {
  const [pageId, setPageId] = useState(pages[0].id);
  const page = pages.find((p) => p.id === pageId) ?? pages[0];
  const open = (id: string) => {
    setPageId(id);
    window.scrollTo(0, 0);
  };
  return (
    <div className="docs">
      <div className="docs-toc" role="navigation" aria-label="目次">
        <p className="eyebrow">ページ</p>
        <div className="docs-pages">
          {pages.map((p) => (
            <button
              key={p.id}
              className={p.id === page.id ? "selected" : ""}
              aria-current={p.id === page.id ? "page" : undefined}
              onClick={() => open(p.id)}
            >
              {p.title}
            </button>
          ))}
        </div>
        <p className="eyebrow">目次</p>
        {page.toc.map((t) => (
          <a key={t.id} href={"#" + t.id}>
            {t.text}
          </a>
        ))}
      </div>
      <article className="doc-body" key={page.id}>
        {page.out}
      </article>
    </div>
  );
}
