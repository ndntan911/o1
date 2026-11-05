import type { AnyRouter } from '@tanstack/react-router';
import type { QueryClient } from '@tanstack/react-query';
import { setUnauthenticatedHandler } from './api/client';
import { buildLoginSearch, shouldRedirectToLogin } from './auth-redirect';

export function registerUnauthenticatedHandler(
	router: AnyRouter,
	queryClient: QueryClient,
): void {
	setUnauthenticatedHandler(() => {
		// Clear on a macrotask. The handler fires synchronously inside apiRequest
		// before the query rejection settles; clearing now would cancel the
		// in-flight `me` query and turn the 401 into a CancelledError, so route
		// guards could not recognize it as unauthenticated.
		setTimeout(() => queryClient.clear(), 0);
		const { pathname, searchStr } = router.state.location;
		if (shouldRedirectToLogin(pathname)) {
			void router.navigate({
				to: '/login',
				search: buildLoginSearch(pathname, searchStr),
				replace: true,
			});
		}
	});
}
