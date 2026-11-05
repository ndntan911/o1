import type { ComponentProps } from 'react';
import { describe, expect, test, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithProviders } from '@/test/render';
import type { Monitor } from '@/lib/api/types';
import type { ApiProblem } from '@/lib/api/problems';
import { defaultMonitorValues, monitorToFormValues } from './form';
import { MonitorForm } from './MonitorForm';

const monitor: Monitor = {
	id: 'm1',
	workspace_id: 'w1',
	name: 'API',
	url: 'https://api.example.com/health',
	method: 'POST',
	headers: { Authorization: '***', 'X-Api-Key': '***' },
	has_body: true,
	content_type: 'application/json',
	expected_status: '2xx',
	interval_seconds: 300,
	timeout_seconds: 10,
	confirmation_threshold: 1,
	recovery_threshold: 1,
	paused: false,
	status: 'up',
	last_check: null,
	summary_24h: null,
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z',
};

function renderEditForm(
	overrides: Partial<ComponentProps<typeof MonitorForm>> = {},
) {
	const onSubmit = vi.fn().mockResolvedValue(undefined);

	const view = renderWithProviders(
		<MonitorForm
			mode="edit"
			initialValues={monitorToFormValues(monitor)}
			monitor={monitor}
			submitLabel="Save changes"
			submitting={false}
			onSubmit={onSubmit}
			{...overrides}
		/>,
	);

	return { ...view, onSubmit };
}

describe('MonitorForm header secrets', () => {
	test('lists existing header names read-only without exposing masked values', () => {
		renderEditForm();

		expect(screen.getByText('Authorization')).toBeInTheDocument();
		expect(screen.getByText('X-Api-Key')).toBeInTheDocument();
		expect(screen.queryByText('***')).not.toBeInTheDocument();
		expect(screen.queryByLabelText('Header 1 name')).not.toBeInTheDocument();
	});

	test('Replace headers reveals empty editable rows', async () => {
		const user = userEvent.setup();
		renderEditForm();

		await user.click(screen.getByRole('switch', { name: 'Replace headers' }));
		await user.click(screen.getByRole('button', { name: 'Add header' }));

		expect(screen.getByLabelText('Header 1 name')).toHaveValue('');
		expect(screen.getByLabelText('Header 1 value')).toHaveValue('');
	});

	test('enabling replacement with zero rows warns that all headers will be removed', async () => {
		const user = userEvent.setup();
		renderEditForm();

		expect(
			screen.queryByText('Saving will remove all headers.'),
		).not.toBeInTheDocument();

		await user.click(screen.getByRole('switch', { name: 'Replace headers' }));

		expect(
			screen.getByText('Saving will remove all headers.'),
		).toBeInTheDocument();

		await user.click(screen.getByRole('button', { name: 'Add header' }));

		expect(
			screen.queryByText('Saving will remove all headers.'),
		).not.toBeInTheDocument();
	});
});

describe('MonitorForm body editor', () => {
	test.each(['GET', 'HEAD'] as const)(
		'hides the body editor for %s',
		(method) => {
			renderWithProviders(
				<MonitorForm
					mode="create"
					initialValues={{ ...defaultMonitorValues(), method }}
					submitLabel="Create monitor"
					submitting={false}
					onSubmit={vi.fn()}
				/>,
			);

			expect(screen.queryByLabelText('Body')).not.toBeInTheDocument();
			expect(screen.queryByText('Body configured')).not.toBeInTheDocument();
			expect(screen.queryByLabelText('Content type')).not.toBeInTheDocument();
		},
	);

	test('shows the body editor for POST', () => {
		renderWithProviders(
			<MonitorForm
				mode="create"
				initialValues={{ ...defaultMonitorValues(), method: 'POST' }}
				submitLabel="Create monitor"
				submitting={false}
				onSubmit={vi.fn()}
			/>,
		);

		expect(screen.getByLabelText('Body')).toBeInTheDocument();
		expect(screen.getByLabelText('Content type')).toBeInTheDocument();
	});

	test('edit mode shows Body configured and Keep by default', () => {
		renderEditForm();

		expect(screen.getByText('Body configured')).toBeInTheDocument();
		expect(screen.getByRole('radio', { name: 'Keep' })).toBeChecked();
		expect(screen.queryByLabelText('Body')).not.toBeInTheDocument();
	});

	test('choosing Replace reveals the body fields', async () => {
		const user = userEvent.setup();
		renderEditForm();

		await user.click(screen.getByRole('radio', { name: 'Replace' }));

		expect(screen.getByLabelText('Body')).toBeInTheDocument();
		expect(screen.getByLabelText('Content type')).toBeInTheDocument();
	});
});

describe('MonitorForm submission', () => {
	test('submits values with keep secrets and never includes masked headers', async () => {
		const user = userEvent.setup();
		const { onSubmit } = renderEditForm();

		await user.click(screen.getByRole('button', { name: 'Save changes' }));

		await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));

		const [values, secrets] = onSubmit.mock.calls[0] ?? [];
		expect(values).toMatchObject({
			name: 'API',
			url: 'https://api.example.com/health',
			method: 'POST',
			headers: [],
			body: '',
		});
		expect(secrets).toEqual({
			replaceHeaders: false,
			bodyMode: 'keep',
		});
		expect(JSON.stringify(values)).not.toContain('***');
	});

	test('passes replace headers and clear body choices to onSubmit', async () => {
		const user = userEvent.setup();
		const { onSubmit } = renderEditForm();

		await user.click(screen.getByRole('switch', { name: 'Replace headers' }));
		await user.click(screen.getByRole('button', { name: 'Add header' }));
		await user.type(screen.getByLabelText('Header 1 name'), 'Authorization');
		await user.type(screen.getByLabelText('Header 1 value'), 'Bearer fresh');
		await user.click(screen.getByRole('radio', { name: 'Clear' }));
		await user.click(screen.getByRole('button', { name: 'Save changes' }));

		await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));

		const [values, secrets] = onSubmit.mock.calls[0] ?? [];
		expect(values).toMatchObject({
			headers: [{ name: 'Authorization', value: 'Bearer fresh' }],
		});
		expect(secrets).toEqual({
			replaceHeaders: true,
			bodyMode: 'clear',
		});
	});
});

describe('MonitorForm server problems', () => {
	test('maps known fields and shows unmapped messages in ProblemAlert', async () => {
		const problem: ApiProblem = {
			type: 'about:blank',
			title: 'Validation failed',
			status: 400,
			code: 'validation_error',
			fields: {
				url: 'url is not allowed',
				mystery: 'Unmapped server message',
			},
		};

		renderWithProviders(
			<MonitorForm
				mode="create"
				initialValues={defaultMonitorValues()}
				submitLabel="Create monitor"
				submitting={false}
				problem={problem}
				onSubmit={vi.fn()}
			/>,
		);

		expect(await screen.findByText('url is not allowed')).toBeInTheDocument();
		expect(screen.getByText('Unmapped server message')).toBeInTheDocument();
		expect(screen.getByText('Validation failed')).toBeInTheDocument();
	});

	test('renders page-level alerts for quota, rate limit, and suspension', () => {
		const problem: ApiProblem = {
			type: 'about:blank',
			title: 'Monitor quota reached',
			status: 403,
			code: 'quota_exceeded',
			detail: 'Upgrade for more monitors.',
		};

		renderWithProviders(
			<MonitorForm
				mode="create"
				initialValues={defaultMonitorValues()}
				submitLabel="Create monitor"
				submitting={false}
				problem={problem}
				onSubmit={vi.fn()}
			/>,
		);

		expect(screen.getByText('Monitor quota reached')).toBeInTheDocument();
		expect(screen.getByText('Upgrade for more monitors.')).toBeInTheDocument();
	});
});
