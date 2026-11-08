import { CircleHelp } from "lucide-react";
import type { CSSProperties } from "react";
import { useTranslation } from "react-i18next";
import { displayTitle, formatPercent, isUnknown, shortHash } from "@/lib/books";
import { cn } from "@/lib/utils";

// Палитра макета Cover: пары (оттенок, светлота) в oklch.
const PALETTE: [number, number][] = [
  [25, 0.4],
  [55, 0.42],
  [85, 0.8],
  [130, 0.4],
  [165, 0.38],
  [200, 0.4],
  [235, 0.4],
  [270, 0.8],
  [305, 0.4],
  [345, 0.42],
];

const SIZES = {
  S: { w: 40, h: 60, radius: 3, spine: 4, pad: "0", icon: 18 },
  M: { w: 120, h: 180, radius: 4, spine: 7, pad: "14px 10px 12px 18px", icon: 26 },
  L: { w: 192, h: 288, radius: 6, spine: 10, pad: "22px 18px 18px 26px", icon: 36 },
} as const;

export type CoverSize = keyof typeof SIZES;

type CoverBook = {
  hash?: string;
  title: string | null;
  authors: string | null;
  filename: string | null;
  percentage?: number;
};

// Цвет — от «название + авторы», а не от хеша: одна книга в FB2 и EPUB получает
// один цвет. Хеш строки тот же, что в макете, чтобы цвета совпадали с дизайном.
function paletteIndex(key: string): number {
  let s = 0;
  for (const c of key) s = (s * 31 + c.charCodeAt(0)) >>> 0;
  return s % PALETTE.length;
}

const shadow = "0 1px 2px oklch(0 0 0 / .25), 0 2px 8px oklch(0 0 0 / .12)";

export function BookCover({ book, size, className }: { book: CoverBook; size: CoverSize; className?: string }) {
  const { i18n } = useTranslation();
  const dim = SIZES[size];
  const small = size === "S";
  const big = size === "L";
  const box: CSSProperties = {
    width: dim.w,
    height: dim.h,
    borderRadius: dim.radius,
    padding: dim.pad,
    boxShadow: shadow,
  };
  const frame = cn("relative flex flex-none flex-col overflow-hidden", className);

  // У книги библиотеки нет хеша: штриховка — только у неизвестной книги прогресса.
  if (isUnknown(book) && book.hash !== undefined) {
    return (
      <div
        className={frame}
        aria-hidden
        style={{
          ...box,
          background: "repeating-linear-gradient(135deg, oklch(0.33 0.006 70) 0 6px, oklch(0.30 0.006 70) 6px 12px)",
          color: "oklch(0.9 0.008 80)",
        }}
      >
        <div
          className={cn("flex flex-1 flex-col opacity-90", small ? "items-center justify-center" : "gap-2")}
          style={{ marginLeft: small ? 0 : big ? -8 : -6 }}
        >
          <CircleHelp size={dim.icon} strokeWidth={1.75} />
          {!small && (
            <>
              <div className={cn("font-mono leading-tight break-all opacity-80", big ? "text-[13px]" : "text-[10px]")}>
                {shortHash(book.hash)}
              </div>
              <div className={cn("mt-auto leading-none font-semibold tabular-nums", big ? "text-[40px]" : "text-2xl")}>
                {formatPercent(book.percentage ?? 0, i18n.language)}
              </div>
            </>
          )}
        </div>
      </div>
    );
  }

  const title = displayTitle(book) ?? "";
  const [hue, light] = PALETTE[paletteIndex(title + "\u0001" + (book.authors ?? ""))] ?? [200, 0.4];
  const isLight = light > 0.6;
  return (
    <div
      className={frame}
      aria-hidden
      style={{
        ...box,
        background: `oklch(${light} ${isLight ? 0.07 : 0.075} ${hue})`,
        color: isLight ? `oklch(0.26 0.04 ${hue})` : `oklch(0.94 0.025 ${hue})`,
      }}
    >
      <div
        className="absolute inset-y-0 left-0"
        style={{
          width: dim.spine,
          background: "linear-gradient(90deg, oklch(0 0 0 / .28), oklch(0 0 0 / .08) 70%, oklch(1 0 0 / .06))",
        }}
      />
      {small ? (
        <div className="m-auto pl-[3px] font-serif text-xl leading-none font-semibold">
          {title.trim().charAt(0).toUpperCase() || "?"}
        </div>
      ) : (
        <>
          <div
            className={cn(
              "overflow-hidden font-serif font-semibold tracking-[-0.005em] text-pretty hyphens-auto [overflow-wrap:anywhere]",
              big ? "line-clamp-6 text-2xl leading-[29px]" : "line-clamp-5 text-[15px] leading-[19px]",
            )}
          >
            {title}
          </div>
          <div
            className="mt-auto h-px bg-current opacity-50"
            style={{ width: big ? 32 : 20, marginBottom: big ? 10 : 7 }}
          />
          <div
            className={cn(
              "line-clamp-2 font-medium tracking-[.07em] uppercase opacity-85",
              big ? "text-xs leading-4" : "text-[9px] leading-3",
            )}
          >
            {book.authors}
          </div>
        </>
      )}
    </div>
  );
}
