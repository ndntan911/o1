import { describe, expect, test, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import type { AdminWorkspaceListResponse } from '@/lib/api/types';
import { WorkspaceCombobox } from './WorkspaceCombobox';

const ORIGIN = 'http://localhost:5173';
const workspacesPath = `${ORIGIN}/api/v1/admin/workspaces`;

const acme = {
	id: 'w1',
	name: 'Acme Status',
	slug: 'acme-status',
	created_by: 'u1',
	created_at: '2026-01-01T00:00:00Z',
	monitor_limit: 10,
	suspended_at: null,
	suspended_by: null,
	suspended_reason: null,
	member_count: 2,
	monitor_count: 3,
};

const beta = {
	...acme,
	id: 'w2',
	name: 'Beta Status',
	slug: 'beta-status',
};

describe('WorkspaceCombobox', () => {
	test('debounces search, renders matches, and returns the selected id', async () => {
		const user = userEvent.setup();
		const requests: URL[] = [];
		const response: AdminWorkspaceListResponse = {
			workspaces: [acme, beta],
		};
		server.use(
			http.get(workspacesPath, ({ request }) => {
				requests.push(new URL(request.url));
				return HttpResponse.json(response);
			}),
		);
		const onChange = vi.fn();

		renderWithProviders(<WorkspaceCombobox value={null} onChange={onChange} />);

		const input = screen.getByLabelText('Workspace');
		expect(input).toHaveAttribute('placeholder', 'All workspaces');

		await user.type(input, 'acme');

		await waitFor(
			() => {
				expect(requests).toHaveLength(1);
				expect(requests[0].searchParams.get('q')).toBe('acme');
			},
			{ timeout: 2000 },
		);

		const option = await screen.findByRole('option', { name: /Acme Status/ });
		await user.click(option);

		expect(onChange).toHaveBeenCalledWith('w1');
		expect(input).toHaveValue('Acme Status');
	});

	test('clears the selection when allowed', async () => {
		const user = userEvent.setup();
		const onChange = vi.fn();

		renderWithProviders(
			<WorkspaceCombobox
				value="w1"
				onChange={onChange}
				label="Workspace"
				allowClear
			/>,
		);

		await user.click(screen.getByRole('button', { name: 'Clear workspace' }));

		expect(onChange).toHaveBeenCalledWith(null);
	});
});
