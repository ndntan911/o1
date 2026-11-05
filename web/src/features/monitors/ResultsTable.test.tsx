import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, test, vi } from 'vitest';
import type { ProbeResult } from '@/lib/api/types';
import { formatRelativeTime } from '@/lib/format';
import { ResultsTable } from './ResultsTable';

const CHECKED_AT = new Date(Date.now() - 5 * 60_000).toISOString();

const result: ProbeResult = {
	id: 'r1',
	checked_at: CHECKED_AT,
	scheduled_for: CHECKED_AT,
	status: 'down',
	status_code: 503,
	latency_ms: 1234.6,
	error: 'connection refused',
};

function useDesktopViewport() {
	vi.spyOn(window, 'matchMedia').mockImplementation(
		(query: string) =>
			({
				matches: query === '(min-width: 768px)',
				media: query,
				onchange: null,
				addListener: () => undefined,
				removeListener: () => undefined,
				addEventListener: () => undefined,
				removeEventListener: () => undefined,
				dispatchEvent: () => false,
			}) as MediaQueryList,
	);
}

function renderTable(
	overrides: Partial<Parameters<typeof ResultsTable>[0]> = {},
) {
	return render(
		<ResultsTable
			results={[result]}
			hasNextPage={false}
			isFetchingNextPage={false}
			onLoadMore={() => undefined}
			{...overrides}
		/>,
	);
}

afterEach(() => {
	vi.restoreAllMocks();
});

describe('ResultsTable', () => {
	test('renders checked-at, status, status code, latency, and error', () => {
		useDesktopViewport();
		renderTable();

		const table = screen.getByRole('table');
		const row = within(table).getByRole('row', { name: /503/ });

		expect(
			within(row).getByText(formatRelativeTime(CHECKED_AT)),
		).toBeInTheDocument();
		expect(within(row).getByText('Down')).toBeInTheDocument();
		expect(within(row).getByText('503')).toBeInTheDocument();
		expect(within(row).getByText('1235 ms')).toBeInTheDocument();
		const error = within(row).getByText('connection refused');
		expect(error).toHaveAttribute('title', 'connection refused');
	});

	test('renders the same data as stacked rows below md', () => {
		renderTable();

		expect(screen.queryByRole('table')).not.toBeInTheDocument();
		expect(
			screen.getByText(formatRelativeTime(CHECKED_AT)),
		).toBeInTheDocument();
		expect(screen.getByText('Down')).toBeInTheDocument();
		expect(screen.getByText('503')).toBeInTheDocument();
		expect(screen.getByText('1235 ms')).toBeInTheDocument();
		expect(screen.getByText('connection refused')).toBeInTheDocument();
	});

	test('shows No results in this window for an empty list', () => {
		renderTable({ results: [] });

		expect(screen.getByText('No results in this window')).toBeInTheDocument();
		expect(
			screen.queryByRole('button', { name: 'Load more' }),
		).not.toBeInTheDocument();
	});

	test('calls onLoadMore from Load more and hides the button when there is no next page', async () => {
		const onLoadMore = vi.fn();
		const user = userEvent.setup();

		const { rerender } = renderTable({ hasNextPage: true, onLoadMore });

		await user.click(screen.getByRole('button', { name: 'Load more' }));
		expect(onLoadMore).toHaveBeenCalledTimes(1);

		rerender(
			<ResultsTable
				results={[result]}
				hasNextPage={false}
				isFetchingNextPage={false}
				onLoadMore={onLoadMore}
			/>,
		);

		expect(
			screen.queryByRole('button', { name: 'Load more' }),
		).not.toBeInTheDocument();
	});

	test('disables Load more while the next page is fetching', () => {
		renderTable({ hasNextPage: true, isFetchingNextPage: true });

		expect(screen.getByRole('button', { name: 'Load more' })).toBeDisabled();
	});
});
