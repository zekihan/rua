import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import "./index.css";
import App from "./App.tsx";

const root = document.getElementById("root");
if (!root) throw new Error("missing #root element");
const qc = new QueryClient({ defaultOptions: { queries: { retry: 1, staleTime: 30000 } } });
createRoot(root).render(<StrictMode><QueryClientProvider client={qc}><App /></QueryClientProvider></StrictMode>);
