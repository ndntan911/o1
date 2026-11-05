import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { QueryClientProvider } from '@tanstack/react-query';
import { RouterProvider } from '@tanstack/react-router';
import './styles.css';
import { setUnauthenticatedHandler } from './lib/api/client';
import { buildLoginSearch, shouldRedirectToLogin } from './lib/auth-redirect';
import { queryClient } from './lib/query/client';
import { router } from './router';

setUnauthenticatedHandler(() => {
	queryClient.clear();
	const { pathname, search } = window.location;
	if (shouldRedirectToLogin(pathname)) {
		void router.navigate({
			to: '/login',
			search: buildLoginSearch(pathname, search),
			replace: true,
		});
	}
});

createRoot(document.getElementById('root')!).render(
	<StrictMode>
		<QueryClientProvider client={queryClient}>
			<RouterProvider router={router} />
		</QueryClientProvider>
	</StrictMode>,
);
