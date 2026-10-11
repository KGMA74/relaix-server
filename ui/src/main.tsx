import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";
import { ApiError } from "./api/client";
import { AuthProvider, useAuth } from "./auth";
import { Layout } from "./components/Layout";
import "./index.css";
import { DevicesPage } from "./pages/Devices";
import { HistoryPage } from "./pages/History";
import { IntegrationPage } from "./pages/Integration";
import { LoginPage } from "./pages/Login";
import { OverviewPage } from "./pages/Overview";
import { SendPage } from "./pages/Send";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // A 4xx will not fix itself on retry; a dropped connection might.
      retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 2,
      refetchOnWindowFocus: true,
    },
  },
});

function App() {
  const { signedIn } = useAuth();
  if (!signedIn) return <LoginPage />;

  return (
    <Routes>
      <Route element={<Layout />}>
        <Route index element={<OverviewPage />} />
        <Route path="send" element={<SendPage />} />
        <Route path="history" element={<HistoryPage />} />
        <Route path="devices" element={<DevicesPage />} />
        <Route path="integration" element={<IntegrationPage />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter basename="/ui">
        <AuthProvider>
          <App />
        </AuthProvider>
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
);
