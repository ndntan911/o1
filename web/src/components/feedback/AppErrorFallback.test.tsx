import type { ReactNode } from 'react';
import { afterEach, describe, expect, test, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { AppErrorBoundary, AppErrorFallback } from './AppErrorFallback';

afterEach(() => {
	vi.restoreAllMocks();
});

describe('AppErrorFallback', () => {
	test('renders a friendly message and reloads the page on request', async () => {
		const onReload = vi.fn();
		const resetError = vi.fn();
		const user = userEvent.setup();

		render(<AppErrorFallback resetError={resetError} onReload={onReload} />);

		expect(
			screen.getByRole('heading', { name: /something went wrong/i }),
		).toBeInTheDocument();
		expect(screen.getByText(/reloading the page/i)).toBeInTheDocument();

		await user.click(screen.getByRole('button', { name: /reload page/i }));

		expect(onReload).toHaveBeenCalledTimes(1);
		expect(resetError).toHaveBeenCalledTimes(1);
	});
});

describe('AppErrorBoundary', () => {
	test('shows the fallback when a child throws', () => {
		vi.spyOn(console, 'error').mockImplementation(() => undefined);

		function Boom(): ReactNode {
			throw new Error('boom');
		}

		render(
			<AppErrorBoundary>
				<Boom />
			</AppErrorBoundary>,
		);

		expect(
			screen.getByRole('heading', { name: /something went wrong/i }),
		).toBeInTheDocument();
		expect(
			screen.getByRole('button', { name: /reload page/i }),
		).toBeInTheDocument();
	});
});
