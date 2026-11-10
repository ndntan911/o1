import type { ReactNode } from 'react';
import * as Sentry from '@sentry/react';
import { Button } from '@/components/ui/button';

export interface AppErrorFallbackProps {
	error?: unknown;
	resetError?: () => void;
	/** Overridable for tests; defaults to reloading the page. */
	onReload?: () => void;
}

/**
 * Friendly screen shown when a render error escapes the React tree.
 *
 * Errors are reported by `Sentry.ErrorBoundary`; this only gives the user a
 * way out.
 */
export function AppErrorFallback({
	resetError,
	onReload,
}: AppErrorFallbackProps) {
	const reload = () => {
		resetError?.();
		if (onReload) {
			onReload();
			return;
		}
		window.location.reload();
	};

	return (
		<div
			role="alert"
			className="mx-auto flex min-h-screen max-w-md flex-col items-center justify-center gap-4 p-6 text-center"
		>
			<h1 className="text-2xl font-semibold">Something went wrong</h1>
			<p className="text-muted-foreground">
				An unexpected error occurred. Reloading the page usually fixes it.
			</p>
			<Button type="button" onClick={reload}>
				Reload page
			</Button>
		</div>
	);
}

/**
 * Error boundary wiring the app to Sentry's reporter and our friendly
 * fallback screen.
 */
export function AppErrorBoundary({ children }: { children: ReactNode }) {
	return (
		<Sentry.ErrorBoundary
			fallback={({ error, resetError }) => (
				<AppErrorFallback error={error} resetError={resetError} />
			)}
		>
			{children}
		</Sentry.ErrorBoundary>
	);
}
