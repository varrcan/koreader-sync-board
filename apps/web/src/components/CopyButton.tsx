import { Check, Copy } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";

// navigator.clipboard есть только в безопасном контексте (HTTPS или localhost),
// а панель часто открывают по http:// в локальной сети. Там копируем через
// execCommand("copy"), подставляя текст в событие copy. Выделение во временном
// textarea не годится: в модальном диалоге Radix возвращает фокус в диалог,
// выделение пропадает, а execCommand всё равно отвечает true с пустым буфером.
async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // ниже — запасной путь
    }
  }
  let written = false;
  const onCopy = (e: ClipboardEvent) => {
    if (!e.clipboardData) return;
    e.clipboardData.setData("text/plain", text);
    e.preventDefault();
    written = true;
  };
  document.addEventListener("copy", onCopy);
  try {
    document.execCommand("copy");
  } finally {
    document.removeEventListener("copy", onCopy);
  }
  return written;
}

export function CopyButton({ text }: { text: string }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  return (
    <Button
      type="button"
      variant="outline"
      size="sm"
      className="shrink-0"
      onClick={() => {
        void copyText(text).then((ok) => {
          if (!ok) return;
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        });
      }}
    >
      {copied ? <Check /> : <Copy />}
      {copied ? t("copy.copied") : t("copy.copy")}
    </Button>
  );
}
