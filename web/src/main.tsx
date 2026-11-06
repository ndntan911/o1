import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { QueryClientProvider } from '@tanstack/react-query';
import { RouterProvider } from '@tanstack/react-router';
import { ThemeProvider } from 'next-themes';
import './styles.css';
import { queryClient } from './lib/query/client';
import { registerUnauthenticatedHandler } from './lib/unauthenticated-handler';
import { router } from './router';

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
				<RouterProvider router={router} />
			</QueryClientProvider>
		</ThemeProvider>
	</StrictMode>,
);
