import { Code2, Hash, History, Inbox, LayoutDashboard, LogOut, Moon, Send, Smartphone, Sun, Workflow, type LucideIcon } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { NavLink, Outlet, useLocation } from "react-router-dom";
import { api } from "@/api/client";
import { useAuth } from "@/auth";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SIDEBAR_COOKIE_NAME,
  SidebarProvider,
  SidebarRail,
  SidebarTrigger,
  useSidebar,
} from "@/components/ui/sidebar";
import { Toaster } from "@/components/ui/sonner";
import { useTheme } from "@/hooks/use-theme";
import { useLive, useLiveEvent, type LiveStatus } from "@/lib/live";
import { cn } from "@/lib/utils";
import { toast } from "sonner";

export interface NavItem {
  to: string;
  label: string;
  icon: LucideIcon;
  end?: boolean;
}

export interface NavGroup {
  label: string;
  items: NavItem[];
}

const groups: NavGroup[] = [
  {
    label: "Messages",
    items: [
      { to: "/", label: "Vue d'ensemble", icon: LayoutDashboard, end: true },
      { to: "/send", label: "Envoyer", icon: Send },
      { to: "/history", label: "Historique", icon: History },
      { to: "/inbox", label: "Reçus", icon: Inbox },
      { to: "/ussd", label: "USSD", icon: Hash },
    ],
  },
  {
    label: "Flotte",
    items: [
      { to: "/devices", label: "Appareils", icon: Smartphone },
      { to: "/automations", label: "Automatisations", icon: Workflow },
      { to: "/integration", label: "Intégration", icon: Code2 },
    ],
  },
];

function isActive(pathname: string, item: NavItem) {
  return item.end ? pathname === item.to : pathname === item.to || pathname.startsWith(item.to + "/");
}

/**
 * The sidebar writes its open state to this cookie on every toggle but never
 * reads it back; without this, a collapsed sidebar reopens on each reload.
 */
function readSidebarOpen(): boolean {
  return !document.cookie.split("; ").includes(`${SIDEBAR_COOKIE_NAME}=false`);
}

/**
 * "2 hors ligne" beside Appareils: enabled phones that are not connected. It
 * shares the devices query with the pages, so it costs no request of its own.
 */
function useOfflineBadge(): string | undefined {
  const devices = useQuery({ queryKey: ["devices"], queryFn: api.listDevices, staleTime: 30_000 });
  const n = devices.data?.devices.filter((d) => d.enabled && !d.connected).length ?? 0;
  return n > 0 ? `${n} hors ligne` : undefined;
}

function Nav({ groups }: { groups: NavGroup[] }) {
  const { pathname } = useLocation();
  const { isMobile, setOpenMobile } = useSidebar();
  const offline = useOfflineBadge();

  return (
    <>
      {groups.map((group) => (
        <SidebarGroup key={group.label}>
          <SidebarGroupLabel className="text-section text-sidebar-foreground/80 uppercase">{group.label}</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              {group.items.map((item) => (
                <SidebarMenuItem key={item.to}>
                  <SidebarMenuButton
                    asChild
                    isActive={isActive(pathname, item)}
                    tooltip={item.label}
                    // The active row: raised, outlined, its icon in the accent.
                    className="h-[34px] data-[active=true]:shadow-[inset_0_0_0_1px_var(--sidebar-border)] data-[active=true]:[&>svg]:text-primary"
                  >
                    <NavLink to={item.to} end={item.end} onClick={() => isMobile && setOpenMobile(false)}>
                      <item.icon aria-hidden />
                      <span>{item.label}</span>
                      {item.to === "/devices" && offline && (
                        <span className="ml-auto inline-flex shrink-0 items-center gap-1 rounded-full bg-danger-soft px-1.5 text-[11px] leading-5 font-medium text-destructive group-data-[collapsible=icon]:hidden">
                          <span className="size-1.5 rounded-full bg-destructive" aria-hidden />
                          {offline}
                        </span>
                      )}
                    </NavLink>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              ))}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      ))}
    </>
  );
}

/** Whether the dashboard is receiving live events, in the top bar. */
function LiveIndicator() {
  const { status } = useLive();
  const label: Record<LiveStatus, string> = {
    live: "En direct",
    connecting: "Connexion…",
    offline: "Hors ligne",
  };
  const title: Record<LiveStatus, string> = {
    live: "Les changements s'affichent dès qu'ils arrivent.",
    connecting: "Ouverture du flux d'événements…",
    offline: "Flux d'événements coupé : actualisation périodique en attendant de le rétablir.",
  };
  return (
    <span
      title={title[status]}
      className={cn(
        "hidden h-7 items-center gap-2 rounded-full border px-3 text-xs font-medium sm:inline-flex",
        status === "offline" && "text-muted-foreground",
      )}
    >
      <span
        aria-hidden
        className={cn(
          "size-[7px] rounded-full",
          status === "live" && "bg-success shadow-[0_0_0_3px_var(--color-success-soft)]",
          status === "connecting" && "bg-warning shadow-[0_0_0_3px_var(--color-warning-soft)]",
          status === "offline" && "bg-muted-foreground",
        )}
      />
      {label[status]}
    </span>
  );
}

/** Announces the fleet changes an operator should notice even on another page. */
function LiveToasts() {
  useLiveEvent((e) => {
    if (e.type === "device.enrolled") {
      toast.success(e.reenrolled ? "Téléphone réenrôlé" : "Nouveau téléphone dans la flotte", {
        description: e.reenrolled ? "Il a repris sa fiche et son historique." : undefined,
      });
    } else if (e.type === "device.removed") {
      toast("Un téléphone a quitté la flotte");
    } else if (e.type === "job.updated" && e.status === "failed") {
      toast.error("Un envoi a échoué", { description: "Le détail est dans l'historique." });
    }
  });
  return null;
}

function ThemeToggle({ theme, toggle }: { theme: "light" | "dark"; toggle: () => void }) {
  return (
    <Button
      variant="outline"
      size="icon-sm"
      onClick={toggle}
      aria-label={theme === "dark" ? "Passer au thème clair" : "Passer au thème sombre"}
      title={theme === "dark" ? "Thème clair" : "Thème sombre"}
    >
      {theme === "dark" ? <Sun aria-hidden /> : <Moon aria-hidden />}
    </Button>
  );
}

/**
 * What an app embedding these pages may change: its name, extra sections of
 * navigation after the gateway's own, and what sits above "Se déconnecter".
 */
export interface LayoutProps {
  brand?: { name: string; tagline?: string };
  extraGroups?: NavGroup[];
  footer?: ReactNode;
  /** Who is signed in, in the sidebar's foot. Omitted: the operator, by API key. */
  user?: { name: string; role?: string };
}

export function Layout({ brand, extraGroups = [], footer, user }: LayoutProps = {}) {
  const { signOut } = useAuth();
  const [theme, toggleTheme] = useTheme();
  const { pathname } = useLocation();
  const allGroups = [...groups, ...extraGroups];
  const currentGroup = allGroups.find((g) => g.items.some((i) => isActive(pathname, i)));
  const current = currentGroup?.items.find((i) => isActive(pathname, i));
  const name = brand?.name ?? "Relaix";

  return (
    // Bounded to the viewport so only the content column scrolls, never the sidebar with it.
    <SidebarProvider defaultOpen={readSidebarOpen()} className="h-svh overflow-hidden">
      <Sidebar collapsible="icon">
        <SidebarHeader>
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton size="lg" asChild tooltip={name}>
                <NavLink to="/">
                  <span className="inline-flex size-8 shrink-0 items-center justify-center rounded-lg bg-primary text-white" aria-hidden>
                    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                      <path d="M4 17V8a3 3 0 0 1 3-3h10a3 3 0 0 1 3 3v6a3 3 0 0 1-3 3h-6l-5 3z" />
                      <path d="M9 11h.01M12.5 11h.01M16 11h.01" />
                    </svg>
                  </span>
                  <div className="grid flex-1 text-left leading-tight">
                    <span className="truncate font-semibold">{name}</span>
                    <span className="truncate text-xs text-sidebar-foreground/60">{brand?.tagline ?? "Passerelle SMS"}</span>
                  </div>
                </NavLink>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarHeader>

        <SidebarContent>
          <Nav groups={allGroups} />
        </SidebarContent>

        <SidebarFooter>
          {footer}
          {/* Who is signed in, and the way out. Folded, only the way out. */}
          <div className="flex items-center gap-2.5 rounded-[10px] border bg-card p-2.5 group-data-[collapsible=icon]:border-0 group-data-[collapsible=icon]:bg-transparent group-data-[collapsible=icon]:p-0">
            <span
              aria-hidden
              className="inline-flex size-8 shrink-0 items-center justify-center rounded-full bg-primary-soft text-[13px] font-semibold text-primary group-data-[collapsible=icon]:hidden"
            >
              {(user?.name ?? "O").charAt(0).toUpperCase()}
            </span>
            <span className="flex min-w-0 flex-1 flex-col group-data-[collapsible=icon]:hidden">
              <span className="truncate text-[13px] font-medium" title={user?.name}>
                {user?.name ?? "Opérateur"}
              </span>
              <span className="truncate text-xs text-muted-foreground">{user?.role ?? "Clé API"}</span>
            </span>
            <Button variant="ghost" size="icon-sm" onClick={signOut} aria-label="Se déconnecter" title="Se déconnecter">
              <LogOut aria-hidden />
            </Button>
          </div>
        </SidebarFooter>
        <SidebarRail />
      </Sidebar>

      <SidebarInset className="min-h-0 overflow-hidden">
        <header className="flex h-14 shrink-0 items-center gap-2 border-b bg-background/80 px-4 backdrop-blur">
          <SidebarTrigger className="-ml-1" title="Réduire / déplier (Ctrl+B)" />
          <Separator orientation="vertical" className="mr-1 data-[orientation=vertical]:h-4" />
          <nav aria-label="Fil d'Ariane" className="flex min-w-0 items-center gap-2 text-[13px]">
            {currentGroup && (
              <>
                <span className="hidden text-muted-foreground sm:inline">{currentGroup.label}</span>
                <span className="hidden text-muted-foreground/70 sm:inline" aria-hidden>
                  /
                </span>
              </>
            )}
            <span className="truncate font-medium">{current?.label ?? name}</span>
          </nav>
          <div className="ml-auto flex items-center gap-2">
            <LiveIndicator />
            <ThemeToggle theme={theme} toggle={toggleTheme} />
          </div>
        </header>
        {/* min-w-0: without it a flex child never shrinks below its content, and
            the whole page scrolls sideways instead of just the wide table. */}
        <div key={pathname} className="animate-page-enter min-h-0 w-full min-w-0 flex-1 overflow-y-auto">
          <div className="mx-auto max-w-[1200px] px-4 pt-7 pb-14 md:px-8">
            <Outlet />
          </div>
        </div>
      </SidebarInset>
      <LiveToasts />
      <Toaster theme={theme} position="bottom-right" richColors closeButton />
    </SidebarProvider>
  );
}

