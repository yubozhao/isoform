import type { ReactNode } from "react";
import { LogoIcon } from "./LogoIcon";
import { cn } from "@/lib/utils";
import { useTranslation } from "react-i18next";

interface LogoWithFontIconProps {
    className?: string;
    height?: number;
    /** Play a quick double blink of the dot when the logo is hovered. */
    blinkOnHover?: boolean;
    /** Draws this 19-unit mark in place of the still logo (the sidebar's
     *  animated mark); the wordmark beside it never moves. */
    mark?: ReactNode;
}

export function LogoWithFontIcon({ className, height = 19, blinkOnHover, mark }: LogoWithFontIconProps) {
    const { t } = useTranslation('common');
    const width = (90 / 19) * height;
    return (
        <svg
            width={width}
            height={height}
            viewBox="0 0 90 19"
            fill="none"
            xmlns="http://www.w3.org/2000/svg"
            className={cn(className, 'text-accent', blinkOnHover && 'logo-hover-blink')}
        >
            {mark ?? <LogoIcon size={19} eyeClassName={blinkOnHover ? "logo-eye-hover" : undefined} />}
            {/* Wordmark: the product name is Isoform (the letter paths spelled
                the old name). A fixed textLength keeps the width stable
                whatever font the WebView resolves. */}
            <text
                x="25"
                y="14"
                fill="var(--color-accent)"
                fontSize="12.5"
                fontWeight={500}
                letterSpacing="1.1"
                textLength="63"
                lengthAdjust="spacingAndGlyphs"
                style={{ fontFamily: "inherit" }}
            >
                {t('brand.wordmark')}
            </text>
        </svg>
    );
}
