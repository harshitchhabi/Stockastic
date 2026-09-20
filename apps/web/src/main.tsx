import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { SessionProvider } from "@/lib/session";
import { ErrorBoundary } from "@/components/ErrorBoundary";
import App from "./App";
import "./globals.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ErrorBoundary name="Stockastic">
      <SessionProvider>
        <App />
      </SessionProvider>
    </ErrorBoundary>
  </StrictMode>
);
