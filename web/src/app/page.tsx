"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { LoaderCircle } from "lucide-react";

export default function HomePage() {
  const router = useRouter();
  useEffect(() => { router.replace("/dashboard/"); }, [router]);
  return <main className="grid min-h-dvh place-items-center bg-page"><LoaderCircle aria-label="Loading" className="size-5 animate-spin text-fg-muted" /></main>;
}
