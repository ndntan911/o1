import type { AnyRouter } from '@tanstack/react-router';
import type { QueryClient } from '@tanstack/react-query';
import { setUnauthenticatedHandler } from './api/client';
import { buildLoginSearch, shouldRedirectToLogin } from './auth-redirect';
import { queryKeys } from './query/keys';

/**
 * Drop cached `me` data before navigating. `ensureQueryData` returns cached
 * data whenever it exists, even after a failed background refetch, so a stale
 * `Me` would let a route guard redirect back into the authenticated app.
 * Using `setState` (rather than removing the query) keeps any in-flight `me`
 * fetch alive, so initial logged-out requests still reject with the real 401
 * `ApiError` instead of React Query's `CancelledError`.
 */
function evictCachedMe(queryClient: QueryClient): void {
	const meQuery = queryClient.getQueryCache().find({ queryKey: queryKeys.me });
	if (meQuery?.state.data !== undefined) {
		meQuery.setState({ data: undefined });
	}
}

export function registerUnauthenticatedHandler(
	router: AnyRouter,
	queryClient: QueryClient,
): void {
	setUnauthenticatedHandler(() => {
		evictCachedMe(queryClient);
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
