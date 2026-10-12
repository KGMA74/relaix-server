import { useQueryClient } from "@tanstack/react-query";
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { getApiKey, setApiKey, setUnauthorizedHandler } from "./api/client";

export interface Auth {
  signedIn: boolean;
  signIn: (key: string) => void;
  signOut: () => void;
}

/**
 * Provided by AuthProvider below, or by an app embedding these pages that signs
 * in some other way: the layout only needs signOut.
 */
export const AuthContext = createContext<Auth | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const [signedIn, setSignedIn] = useState(() => getApiKey() !== null);

  const signOut = useCallback(() => {
    setApiKey(null);
    // Cached responses belong to the key that fetched them.
    queryClient.clear();
    setSignedIn(false);
  }, [queryClient]);

  const signIn = useCallback((key: string) => {
    setApiKey(key);
    setSignedIn(true);
  }, []);

  // A key revoked or rotated server-side shows up as a 401 on the next call.
  useEffect(() => setUnauthorizedHandler(signOut), [signOut]);

  const value = useMemo(() => ({ signedIn, signIn, signOut }), [signedIn, signIn, signOut]);
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): Auth {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth outside AuthProvider");
  return ctx;
}
