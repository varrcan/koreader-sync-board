import type { Translation, TranslationKey } from "@ksb/i18n";
import { Link } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import type { ReactElement, ReactNode } from "react";
import { Trans, useTranslation } from "react-i18next";
import { menuComponents } from "@/components/KoreaderSetup";
import { cn } from "@/lib/utils";

export const helpLink = "font-medium text-foreground underline underline-offset-3 hover:decoration-2";

type Components = Record<string, ReactElement>;

/**
 * Текст справки из перевода. Пункты меню KOReader в строке — <autoSync/>, ссылки и
 * прочие вставки передаются в components; <strong> работает без объявления. Имя вставки
 * не должно совпадать с пустым тегом HTML (link, img, br…): парсер Trans закрывает такой
 * тег сразу, и текст ссылки оказывается снаружи.
 */
export function HelpTrans({ k, components }: { k: TranslationKey; components?: Components }) {
  return <KeyTrans i18nKey={k} components={{ ...menuComponents, ...components }} />;
}

// Ключ уже проверен типом пропа k. Вывод дженериков Trans по объединению всех ключей
// каталога упирается в предел сложности TypeScript (TS2590), поэтому сигнатура сужена.
const KeyTrans = Trans as (props: { i18nKey: TranslationKey; components: Components }) => ReactElement;

export function HelpText({
  k,
  components,
  className,
}: {
  k: TranslationKey;
  components?: Components;
  className?: string;
}) {
  return (
    <p className={cn("text-pretty", className)}>
      <HelpTrans k={k} components={components} />
    </p>
  );
}

/** Страница раздела: ссылка к оглавлению, заголовок, вступление и разделы. */
export function HelpPage({ title, intro, children }: { title: string; intro: TranslationKey; children: ReactNode }) {
  const { t } = useTranslation();
  return (
    <article className="flex max-w-3xl flex-col gap-9">
      <header className="flex flex-col gap-3">
        <Link
          to="/help"
          className="flex items-center gap-1.5 self-start text-sm text-muted-foreground hover:text-foreground [&_svg]:size-4"
        >
          <ArrowLeft />
          {t("nav.help")}
        </Link>
        <h1 className="font-serif text-[clamp(24px,4vw,30px)] leading-tight font-medium">{title}</h1>
        <HelpText k={intro} className="text-[15px] leading-[22px] text-muted-foreground" />
      </header>
      {children}
    </article>
  );
}

/** Раздел с постоянным якорем: id не зависит от языка, на него ссылаются другие экраны. */
export function HelpSection({ id, title, children }: { id: string; title: ReactNode; children: ReactNode }) {
  return (
    <section id={id} className="flex scroll-mt-6 flex-col gap-3">
      <h2 className="text-lg leading-snug font-medium text-balance">
        <a href={`#${id}`} className="hover:underline hover:underline-offset-4">
          {title}
        </a>
      </h2>
      <div className="flex flex-col gap-3 text-[15px] leading-6 text-muted-foreground">{children}</div>
    </section>
  );
}

type Problem = Exclude<keyof Translation["help"]["troubleshooting"], "intro">;

/** Проблема на странице «Решение проблем»: симптом в заголовке, причина и что сделать. */
export function ProblemItem({
  id,
  problem,
  components,
  children,
}: {
  id: string;
  problem: Problem;
  components?: Components;
  children?: ReactNode;
}) {
  const { t } = useTranslation();
  return (
    <HelpSection id={id} title={<HelpTrans k={`help.troubleshooting.${problem}.title`} />}>
      <dl className="flex flex-col gap-3">
        <div className="flex flex-col gap-0.5">
          <dt className="text-xs font-medium tracking-wider uppercase">{t("help.cause")}</dt>
          <dd className="text-pretty">
            <HelpTrans k={`help.troubleshooting.${problem}.cause`} components={components} />
          </dd>
        </div>
        <div className="flex flex-col gap-0.5">
          <dt className="text-xs font-medium tracking-wider uppercase">{t("help.fix")}</dt>
          <dd className="text-pretty">
            <HelpTrans k={`help.troubleshooting.${problem}.fix`} components={components} />
          </dd>
        </div>
      </dl>
      {children}
    </HelpSection>
  );
}
