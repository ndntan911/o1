/* eslint-disable react-refresh/only-export-components -- route modules export the router alongside route components. */
import { lazy, useEffect } from 'react';
import type { QueryClient } from '@tanstack/react-query';
import {
	Outlet,
	createRootRouteWithContext,
	createRoute,
	createRouter,
	isRedirect,
	notFound,
	redirect,
	type RouterHistory,
} from '@tanstack/react-router';
import { RootLayout } from '@/components/layout/RootLayout';
import { AppShell } from '@/components/layout/AppShell';
import { PageSkeleton } from '@/components/feedback/Skeletons';
import { ForgotPasswordScreen } from '@/features/auth/ForgotPasswordScreen';
import { LoginScreen } from '@/features/auth/LoginScreen';
import { PublicLayout } from '@/features/auth/PublicLayout';
import { RegisterScreen } from '@/features/auth/RegisterScreen';
import { ResetPasswordScreen } from '@/features/auth/ResetPasswordScreen';
import { VerifyEmailScreen } from '@/features/auth/VerifyEmailScreen';
import { findWorkspace, isUnauthenticated } from '@/features/auth/guards';
import { parseMonitorWindow } from '@/features/monitors/window';
import { buildLoginSearch } from '@/lib/auth-redirect';
import type { ResultsWindow } from '@/lib/api/types';
import {
	readLastWorkspaceSlug,
	writeLastWorkspaceSlug,
} from '@/lib/last-workspace';
import { queryClient as appQueryClient } from '@/lib/query/client';
import { meQueryOptions, useMe } from '@/lib/query/me';
import { canManageMonitors } from '@/lib/roles';

const WorkspaceListScreen = lazy(async () => {
	const module = await import('@/features/workspaces/WorkspaceListScreen');
	return { default: module.WorkspaceListScreen };
});

const MembersScreen = lazy(async () => {
	const module = await import('@/features/workspaces/MembersScreen');
	return { default: module.MembersScreen };
});

const MonitorListScreen = lazy(async () => {
	const module = await import('@/features/monitors/MonitorListScreen');
	return { default: module.MonitorListScreen };
});

const MonitorCreateScreen = lazy(async () => {
	const module = await import('@/features/monitors/MonitorCreateScreen');
	return { default: module.MonitorCreateScreen };
});

const MonitorEditScreen = lazy(async () => {
	const module = await import('@/features/monitors/MonitorEditScreen');
	return { default: module.MonitorEditScreen };
});

const MonitorDetailScreen = lazy(async () => {
	const module = await import('@/features/monitors/MonitorDetailScreen');
	return { default: module.MonitorDetailScreen };
});

const AdminWorkspacesScreen = lazy(async () => {
	const module = await import('@/features/admin/AdminWorkspacesScreen');
	return { default: module.AdminWorkspacesScreen };
});

const AdminMonitorsScreen = lazy(async () => {
	const module = await import('@/features/admin/AdminMonitorsScreen');
	return { default: module.AdminMonitorsScreen };
});

const SecurityEventsScreen = lazy(async () => {
	const module = await import('@/features/admin/SecurityEventsScreen');
	return { default: module.SecurityEventsScreen };
});

const rootRoute = createRootRouteWithContext<{ queryClient: QueryClient }>()({
	component: RootLayout,
});

const publicLayoutRoute = createRoute({
	getParentRoute: () => rootRoute,
	id: 'public',
	beforeLoad: async ({ context }) => {
		try {
			await context.queryClient.ensureQueryData(meQueryOptions());
			throw redirect({ to: '/' });
		} catch (error) {
			if (isRedirect(error)) throw error;
			if (!isUnauthenticated(error)) throw error;
		}
	},
	component: PublicLayout,
});

const loginRoute = createRoute({
	getParentRoute: () => publicLayoutRoute,
	path: '/login',
	validateSearch: (search: Record<string, unknown>): { redirect?: string } => ({
		redirect: typeof search.redirect === 'string' ? search.redirect : undefined,
	}),
	component: function LoginRoute() {
		const { redirect: redirectTo } = loginRoute.useSearch();
		return <LoginScreen redirectTo={redirectTo} />;
	},
});

const registerRoute = createRoute({
	getParentRoute: () => publicLayoutRoute,
	path: '/register',
	component: RegisterScreen,
});

const verifyEmailRoute = createRoute({
	getParentRoute: () => publicLayoutRoute,
	path: '/verify-email',
	validateSearch: (search: Record<string, unknown>): { token?: string } => ({
		token: typeof search.token === 'string' ? search.token : undefined,
	}),
	component: function VerifyEmailRoute() {
		const { token } = verifyEmailRoute.useSearch();
		return <VerifyEmailScreen token={token} />;
	},
});

const forgotPasswordRoute = createRoute({
	getParentRoute: () => publicLayoutRoute,
	path: '/forgot-password',
	component: ForgotPasswordScreen,
});

const resetPasswordRoute = createRoute({
	getParentRoute: () => publicLayoutRoute,
	path: '/reset-password',
	validateSearch: (search: Record<string, unknown>): { token?: string } => ({
		token: typeof search.token === 'string' ? search.token : undefined,
	}),
	component: function ResetPasswordRoute() {
		const { token } = resetPasswordRoute.useSearch();
		return <ResetPasswordScreen token={token} />;
	},
});

const appLayoutRoute = createRoute({
	getParentRoute: () => rootRoute,
	id: 'app',
	beforeLoad: async ({ context, location }) => {
		try {
			await context.queryClient.ensureQueryData(meQueryOptions());
		} catch (error) {
			if (isUnauthenticated(error)) {
				throw redirect({
					to: '/login',
					search: buildLoginSearch(location.pathname, location.searchStr),
					replace: true,
				});
			}
			throw error;
		}
	},
	component: AppShell,
});

const homeRoute = createRoute({
	getParentRoute: () => appLayoutRoute,
	path: '/',
	beforeLoad: async ({ context }) => {
		const me = await context.queryClient.ensureQueryData(meQueryOptions());
		const lastSlug = readLastWorkspaceSlug();
		const lastWorkspace = lastSlug ? findWorkspace(me, lastSlug) : undefined;
		const workspace = lastWorkspace ?? me.workspaces[0];

		if (!workspace) throw redirect({ to: '/workspaces' });

		throw redirect({
			to: '/w/$workspaceSlug/monitors',
			params: { workspaceSlug: workspace.slug },
		});
	},
});

function WorkspacesRoute() {
	const me = useMe();

	if (!me.data) return <PageSkeleton />;

	return <WorkspaceListScreen workspaces={me.data.workspaces} />;
}

const workspacesRoute = createRoute({
	getParentRoute: () => appLayoutRoute,
	path: '/workspaces',
	component: WorkspacesRoute,
});

function WorkspaceLayoutRoute() {
	const { workspace } = workspaceRoute.useRouteContext();

	useEffect(() => {
		writeLastWorkspaceSlug(workspace.slug);
	}, [workspace.slug]);

	return <Outlet />;
}

const workspaceRoute = createRoute({
	getParentRoute: () => appLayoutRoute,
	path: '/w/$workspaceSlug',
	beforeLoad: async ({ context, params }) => {
		const me = await context.queryClient.ensureQueryData(meQueryOptions());
		const workspace = findWorkspace(me, params.workspaceSlug);
		if (!workspace) throw notFound();
		return { workspace };
	},
	component: WorkspaceLayoutRoute,
});

function MonitorsRoute() {
	const { workspace } = workspaceRoute.useRouteContext();

	return <MonitorListScreen workspace={workspace} />;
}

const monitorsRoute = createRoute({
	getParentRoute: () => workspaceRoute,
	path: '/monitors',
	component: MonitorsRoute,
});

function MonitorCreateRoute() {
	const { workspace } = workspaceRoute.useRouteContext();

	return <MonitorCreateScreen workspace={workspace} />;
}

const monitorCreateRoute = createRoute({
	getParentRoute: () => workspaceRoute,
	path: '/monitors/new',
	beforeLoad: ({ context }) => {
		if (!canManageMonitors(context.workspace.role)) {
			throw redirect({
				to: '/w/$workspaceSlug/monitors',
				params: { workspaceSlug: context.workspace.slug },
			});
		}
	},
	component: MonitorCreateRoute,
});

function MonitorDetailRoute() {
	const { workspace } = workspaceRoute.useRouteContext();
	const { monitorId } = monitorDetailRoute.useParams();

	return <MonitorDetailScreen workspace={workspace} monitorId={monitorId} />;
}

const monitorDetailRoute = createRoute({
	getParentRoute: () => workspaceRoute,
	path: '/monitors/$monitorId',
	validateSearch: (
		search: Record<string, unknown>,
	): { window?: ResultsWindow } => ({
		window: parseMonitorWindow(search.window),
	}),
	component: MonitorDetailRoute,
});

function MonitorEditRoute() {
	const { workspace } = workspaceRoute.useRouteContext();
	const { monitorId } = monitorEditRoute.useParams();

	return <MonitorEditScreen workspace={workspace} monitorId={monitorId} />;
}

const monitorEditRoute = createRoute({
	getParentRoute: () => workspaceRoute,
	path: '/monitors/$monitorId/edit',
	beforeLoad: ({ context }) => {
		if (!canManageMonitors(context.workspace.role)) {
			throw redirect({
				to: '/w/$workspaceSlug/monitors',
				params: { workspaceSlug: context.workspace.slug },
			});
		}
	},
	component: MonitorEditRoute,
});

function MembersRoute() {
	const { workspace } = workspaceRoute.useRouteContext();

	return <MembersScreen workspace={workspace} />;
}

const membersRoute = createRoute({
	getParentRoute: () => workspaceRoute,
	path: '/members',
	component: MembersRoute,
});

function AdminLayoutRoute() {
	return (
		<section className="space-y-6">
			<h1 className="font-heading text-xl font-semibold">Admin</h1>
			<Outlet />
		</section>
	);
}

const adminRoute = createRoute({
	getParentRoute: () => appLayoutRoute,
	path: '/admin',
	beforeLoad: async ({ context }) => {
		const me = await context.queryClient.ensureQueryData(meQueryOptions());
		if (!me.user.is_admin) throw redirect({ to: '/' });
	},
	component: AdminLayoutRoute,
});

const adminWorkspacesRoute = createRoute({
	getParentRoute: () => adminRoute,
	path: '/workspaces',
	component: AdminWorkspacesScreen,
});

const adminMonitorsRoute = createRoute({
	getParentRoute: () => adminRoute,
	path: '/monitors',
	component: AdminMonitorsScreen,
});

const adminSecurityEventsRoute = createRoute({
	getParentRoute: () => adminRoute,
	path: '/security-events',
	component: SecurityEventsScreen,
});

export const routeTree = rootRoute.addChildren([
	publicLayoutRoute.addChildren([
		loginRoute,
		registerRoute,
		verifyEmailRoute,
		forgotPasswordRoute,
		resetPasswordRoute,
	]),
	appLayoutRoute.addChildren([
		homeRoute,
		workspacesRoute,
		workspaceRoute.addChildren([
			monitorsRoute,
			monitorCreateRoute,
			monitorDetailRoute,
			monitorEditRoute,
			membersRoute,
		]),
		adminRoute.addChildren([
			adminWorkspacesRoute,
			adminMonitorsRoute,
			adminSecurityEventsRoute,
		]),
	]),
]);

export interface CreateAppRouterOptions {
	history?: RouterHistory;
	queryClient?: QueryClient;
}

export function createAppRouter(options: CreateAppRouterOptions = {}) {
	return createRouter({
		routeTree,
		context: { queryClient: options.queryClient ?? appQueryClient },
		defaultPreload: 'intent',
		history: options.history,
	});
}

export const router = createAppRouter();

declare module '@tanstack/react-router' {
	interface Register {
		router: typeof router;
	}
}
