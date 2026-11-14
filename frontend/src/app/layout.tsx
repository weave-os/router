import "./globals.css";

import type { Metadata } from "next";

import { TooltipProvider } from "@/shared/components/molecules/Tooltip";

import localFont from "next/font/local";

const geist = localFont({
  display: "swap",
  src: [
    { path: "./fonts/Geist[wght].woff2", style: "normal", weight: "100 900" },
    { path: "./fonts/Geist-Italic[wght].woff2", style: "italic", weight: "100 900" },
  ],
  variable: "--font-geist",
});

export const metadata: Metadata = {
  title: "Weave Router",
  description: "Router dashboard and settings",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" className={geist.variable}>
      <body className="h-screen overflow-hidden font-sans antialiased">
        <TooltipProvider>{children}</TooltipProvider>
      </body>
    </html>
  );
}
