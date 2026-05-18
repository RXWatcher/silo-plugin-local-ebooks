import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClientProvider } from "@tanstack/react-query";
import { queryClient } from "@/lib/queryClient";
import { Toaster } from "@/components/ui/sonner";
import App from "./App";
import "./index.css";
import { getCachedTheme } from "@/lib/api";

const theme = getCachedTheme();
if (theme) {
  document.documentElement.dataset.theme = theme;
  try {
    sessionStorage.setItem("continuum-theme", theme);
  } catch {
    // Ignore storage failures in private browsing contexts.
  }
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <App />
      <Toaster />
    </QueryClientProvider>
  </StrictMode>,
);
