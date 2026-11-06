import { describe, expect, test, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { ApiProblem } from '@/lib/api/problems';
import { ErrorPanel } from './ErrorPanel';

const problem: ApiProblem = {
	type: 'about:blank',
	title: 'Internal Server Error',
	status: 500,
	code: 'internal_error',
	detail: 'The server failed.',
};

describe('ErrorPanel', () => {
	test('renders the message, stable problem code, and retry action', async () => {
		const onRetry = vi.fn();
		const user = userEvent.setup();

		render(
			<ErrorPanel
				title="Could not load monitors"
				message="The server failed."
				problem={problem}
				onRetry={onRetry}
			/>,
		);

		expect(screen.getByText('Could not load monitors')).toBeInTheDocument();
		expect(screen.getByText('The server failed.')).toBeInTheDocument();
		expect(screen.getByText('Code: internal_error')).toBeInTheDocument();

		await user.click(screen.getByRole('button', { name: 'Try again' }));
		expect(onRetry).toHaveBeenCalledTimes(1);
	});

	test('omits the code when no problem is provided', () => {
		render(<ErrorPanel message="Please try again." />);

		expect(screen.getByText('Please try again.')).toBeInTheDocument();
		expect(screen.queryByText(/Code:/)).not.toBeInTheDocument();
	});
});
