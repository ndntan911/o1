/* eslint-disable react-refresh/only-export-components -- route modules export the router alongside route components. */
import { useState } from 'react';
import type { QueryClient } from '@tanstack/react-query';
import {
	Link,
	Outlet,
	createRootRouteWithContext,
	createRoute,
	createRouter,
	isRedirect,
	redirect,
	useNavigate,
	type RouterHistory,
} from '@tanstack/react-router';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { RootLayout } from '@/components/layout/RootLayout';
import { Button } from '@/components/ui/button';
import { ForgotPasswordScreen } from '@/features/auth/ForgotPasswordScreen';
import { LoginScreen } from '@/features/auth/LoginScreen';
import { PublicLayout } from '@/features/auth/PublicLayout';
import { RegisterScreen } from '@/features/auth/RegisterScreen';
import { ResetPasswordScreen } from '@/features/auth/ResetPasswordScreen';
import { VerifyEmailScreen } from '@/features/auth/VerifyEmailScreen';
import { logout } from '@/features/auth/api';
import { isUnauthenticated } from '@/features/auth/guards';
import { buildLoginSearch } from '@/lib/auth-redirect';
import { queryClient as appQueryClient } from '@/lib/query/client';
import { meQueryOptions } from '@/lib/query/me';

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

function AppLayout() {
	const queryClient = useQueryClient();
	const navigate = useNavigate();
	const me = useQuery(meQueryOptions());
	const [loggingOut, setLoggingOut] = useState(false);

	async function handleLogout() {
		setLoggingOut(true);
		try {
			await logout();
		} finally {
			queryClient.clear();
			await navigate({ to: '/login' });
		}
	}

	return (
		<div className="min-h-screen bg-background">
			<header className="flex items-center justify-between border-b px-4 py-3">
				<Link to="/" className="font-medium tracking-tight">
					o1 uptime
				</Link>
				<div className="flex items-center gap-3 text-sm text-muted-foreground">
					<span>{me.data?.user.email}</span>
					<Button
						type="button"
						variant="outline"
						size="sm"
						onClick={() => void handleLogout()}
						disabled={loggingOut}
					>
						{loggingOut ? 'Logging out…' : 'Log out'}
					</Button>
				</div>
			</header>
			<main className="p-4">
				<Outlet />
			</main>
		</div>
	);
}

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
	component: AppLayout,
});

function HomePlaceholder() {
	return (
		<section className="mx-auto max-w-xl space-y-3">
			<h1 className="text-xl font-semibold">o1 uptime dashboard</h1>
			<p className="text-sm text-muted-foreground">
				Your workspaces and monitors will appear here.
			</p>
			<Button asChild>
				<Link to="/workspaces">View workspaces</Link>
			</Button>
		</section>
	);
}

const homeRoute = createRoute({
	getParentRoute: () => appLayoutRoute,
	path: '/',
	component: HomePlaceholder,
});

function WorkspacesPlaceholder() {
	return (
		<section className="mx-auto max-w-xl space-y-3">
			<h1 className="text-xl font-semibold">Workspaces</h1>
			<p className="text-sm text-muted-foreground">
				Workspace management is coming next.
			</p>
		</section>
	);
}

const workspacesRoute = createRoute({
	getParentRoute: () => appLayoutRoute,
	path: '/workspaces',
	component: WorkspacesPlaceholder,
});

export const routeTree = rootRoute.addChildren([
	publicLayoutRoute.addChildren([
		loginRoute,
		registerRoute,
		verifyEmailRoute,
		forgotPasswordRoute,
		resetPasswordRoute,
	]),
	appLayoutRoute.addChildren([homeRoute, workspacesRoute]),
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
