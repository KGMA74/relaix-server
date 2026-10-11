import { Code2, History, LayoutDashboard, LogOut, Moon, Send, Smartphone, Sun, type LucideIcon } from "lucide-react";
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
import { useTheme } from "@/hooks/use-theme";

interface NavItem {
  to: string;
  label: string;
  icon: LucideIcon;
  end?: boolean;
}

const groups: { label: string; items: NavItem[] }[] = [
  {
    label: "Messages",
    items: [
      { to: "/", label: "Vue d'ensemble", icon: LayoutDashboard, end: true },
      { to: "/send", label: "Envoyer", icon: Send },
      { to: "/history", label: "Historique", icon: History },
    ],
  },
  {
    label: "Flotte",
    items: [
      { to: "/devices", label: "Appareils", icon: Smartphone },
      { to: "/integration", label: "Intégration", icon: Code2 },
    ],
  },
];

const allItems = groups.flatMap((g) => g.items);

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

function Nav() {
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

function ThemeToggle() {
  const [theme, toggle] = useTheme();
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

export function Layout() {
  const { signOut } = useAuth();
  const { pathname } = useLocation();
  const current = allItems.find((i) => isActive(pathname, i));

  return (
    // Bounded to the viewport so only the content column scrolls, never the sidebar with it.
    <SidebarProvider defaultOpen={readSidebarOpen()} className="h-svh overflow-hidden">
      <Sidebar collapsible="icon">
        <SidebarHeader>
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton size="lg" asChild tooltip="Relaix">
                <NavLink to="/">
                  <img src="/ui/favicon.svg" alt="" className="size-8 shrink-0 rounded-md" />
                  <div className="grid flex-1 text-left leading-tight">
                    <span className="truncate font-semibold">Relaix</span>
                    <span className="truncate text-xs text-sidebar-foreground/60">Passerelle SMS</span>
                  </div>
                </NavLink>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarHeader>

        <SidebarContent>
          <Nav />
        </SidebarContent>

        <SidebarFooter>
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
          <span className="truncate text-sm font-medium">{current?.label ?? "Relaix"}</span>
          <div className="ml-auto flex items-center gap-1">
            <ThemeToggle />
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
    </SidebarProvider>
  );
}

