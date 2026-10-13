import { Code2, Hash, History, Inbox, LayoutDashboard, LogOut, Moon, Send, Smartphone, Sun, Workflow, type LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { NavLink, Outlet, useLocation } from "react-router-dom";
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

function Nav({ groups }: { groups: NavGroup[] }) {
  const { pathname } = useLocation();
  const { isMobile, setOpenMobile } = useSidebar();

  return (
    <>
      {groups.map((group) => (
        <SidebarGroup key={group.label}>
          <SidebarGroupLabel>{group.label}</SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu>
              {group.items.map((item) => (
                <SidebarMenuItem key={item.to}>
                  <SidebarMenuButton asChild isActive={isActive(pathname, item)} tooltip={item.label}>
                    <NavLink to={item.to} end={item.end} onClick={() => isMobile && setOpenMobile(false)}>
                      <item.icon aria-hidden />
                      <span>{item.label}</span>
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
        "hidden items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs font-medium sm:inline-flex",
        status === "live" && "border-success-line bg-success-soft text-success",
        status === "connecting" && "border-warning-line bg-warning-soft text-warning",
        status === "offline" && "border-border bg-muted text-muted-foreground",
      )}
    >
      <span className="relative flex size-2">
        {status === "live" && (
          <span className="absolute inline-flex size-full animate-ping rounded-full bg-success opacity-60 motion-reduce:hidden" />
        )}
        <span className={cn("relative inline-flex size-2 rounded-full bg-current")} />
      </span>
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
      variant="ghost"
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
}

export function Layout({ brand, extraGroups = [], footer }: LayoutProps = {}) {
  const { signOut } = useAuth();
  const [theme, toggleTheme] = useTheme();
  const { pathname } = useLocation();
  const allGroups = [...groups, ...extraGroups];
  const current = allGroups.flatMap((g) => g.items).find((i) => isActive(pathname, i));
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
                  <img src="/ui/favicon.svg" alt="" className="size-8 shrink-0 rounded-md" />
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
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton onClick={signOut} tooltip="Se déconnecter">
                <LogOut aria-hidden />
                <span>Se déconnecter</span>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarFooter>
        <SidebarRail />
      </Sidebar>

      <SidebarInset className="min-h-0 overflow-hidden">
        <header className="flex h-14 shrink-0 items-center gap-2 border-b bg-background/80 px-4 backdrop-blur">
          <SidebarTrigger className="-ml-1" title="Réduire / déplier (Ctrl+B)" />
          <Separator orientation="vertical" className="mr-1 data-[orientation=vertical]:h-4" />
          <span className="truncate text-sm font-medium">{current?.label ?? name}</span>
          <div className="ml-auto flex items-center gap-2">
            <LiveIndicator />
            <ThemeToggle theme={theme} toggle={toggleTheme} />
          </div>
        </header>
        {/* min-w-0: without it a flex child never shrinks below its content, and
            the whole page scrolls sideways instead of just the wide table. */}
        <div key={pathname} className="animate-page-enter min-h-0 w-full min-w-0 flex-1 overflow-y-auto">
          <div className="mx-auto max-w-6xl px-4 py-6 md:px-8">
            <Outlet />
          </div>
        </div>
      </SidebarInset>
      <LiveToasts />
      <Toaster theme={theme} position="bottom-right" richColors closeButton />
    </SidebarProvider>
  );
}

