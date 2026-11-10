import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { QueryClientProvider } from '@tanstack/react-query';
import { RouterProvider } from '@tanstack/react-router';
import { ThemeProvider } from 'next-themes';
import './styles.css';
import { AppErrorBoundary } from './components/feedback/AppErrorFallback';
import { queryClient } from './lib/query/client';
import { initSentry } from './lib/sentry';
import { registerUnauthenticatedHandler } from './lib/unauthenticated-handler';
import { router } from './router';

initSentry();
registerUnauthenticatedHandler(router, queryClient);

createRoot(document.getElementById('root')!).render(
	<StrictMode>
		<ThemeProvider
			attribute="class"
			defaultTheme="system"
			enableSystem
			storageKey="o1-theme"
			disableTransitionOnChange
		>
			<QueryClientProvider client={queryClient}>
				<AppErrorBoundary>
					<RouterProvider router={router} />
				</AppErrorBoundary>
			</QueryClientProvider>
		</ThemeProvider>
	</StrictMode>,
);
