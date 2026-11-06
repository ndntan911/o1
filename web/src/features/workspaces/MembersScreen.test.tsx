import { beforeEach, describe, expect, test, vi } from 'vitest';
import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { renderWithProviders } from '@/test/render';
import type { Member, Workspace } from '@/lib/api/types';
import { MembersScreen } from './MembersScreen';

const ORIGIN = 'http://localhost:5173';

Element.prototype.hasPointerCapture = () => false;
Element.prototype.setPointerCapture = () => undefined;
Element.prototype.releasePointerCapture = () => undefined;
Element.prototype.scrollIntoView = () => undefined;

const workspace: Workspace = {
	id: 'w1',
	name: 'Acme Status',
	slug: 'acme-status',
	role: 'owner',
};

const members: Member[] = [
	{ user_id: 'u1', email: 'owner@example.com', role: 'owner' },
	{ user_id: 'u2', email: 'member@example.com', role: 'member' },
];

function problem(code: string, status: number, title: string) {
	return HttpResponse.json(
		{
			type: 'about:blank',
			title,
			status,
			code,
		},
		{
			status,
			headers: { 'Content-Type': 'application/problem+json' },
		},
	);
}

function membersHandler(list: Member[] = members) {
	return http.get(`${ORIGIN}/api/v1/workspaces/${workspace.id}/members`, () =>
		HttpResponse.json({ members: list }),
	);
}

async function openAddMember(user: ReturnType<typeof userEvent.setup>) {
	await user.click(await screen.findByRole('button', { name: 'Add member' }));
	return screen.getByRole('dialog');
}

async function roleOptions(user: ReturnType<typeof userEvent.setup>) {
	await user.click(screen.getByLabelText('Role'));
	return screen.getAllByRole('option').map((option) => option.textContent);
}

describe('MembersScreen', () => {
	beforeEach(() => {
		vi.restoreAllMocks();
	});

	test('renders member emails and roles', async () => {
		server.use(membersHandler());

		renderWithProviders(<MembersScreen workspace={workspace} />);

		expect(await screen.findByText('owner@example.com')).toBeInTheDocument();
		expect(screen.getByText('member@example.com')).toBeInTheDocument();
		expect(screen.getByText('owner')).toBeInTheDocument();
		expect(screen.getByText('member')).toBeInTheDocument();
	});

	test('an owner can assign viewer, member, and admin but never owner', async () => {
		server.use(membersHandler());
		const user = userEvent.setup();

		renderWithProviders(<MembersScreen workspace={workspace} />);
		await openAddMember(user);

		expect(await roleOptions(user)).toEqual(['viewer', 'member', 'admin']);
	});

	test('an admin can assign only viewer and member', async () => {
		server.use(membersHandler());
		const user = userEvent.setup();

		renderWithProviders(
			<MembersScreen workspace={{ ...workspace, role: 'admin' }} />,
		);
		await openAddMember(user);

		expect(await roleOptions(user)).toEqual(['viewer', 'member']);
	});

	test.each(['viewer', 'member'] as const)(
		'a %s sees no add-member action',
		async (role) => {
			server.use(membersHandler());

			renderWithProviders(<MembersScreen workspace={{ ...workspace, role }} />);

			expect(await screen.findByText('owner@example.com')).toBeInTheDocument();
			expect(
				screen.queryByRole('button', { name: 'Add member' }),
			).not.toBeInTheDocument();
		},
	);

	test('adds a member with the selected email and role, then refreshes the list', async () => {
		const calls: unknown[] = [];
		let current: Member[] = [members[0]];
		server.use(
			http.get(`${ORIGIN}/api/v1/workspaces/${workspace.id}/members`, () =>
				HttpResponse.json({ members: current }),
			),
			http.post(
				`${ORIGIN}/api/v1/workspaces/${workspace.id}/members`,
				async ({ request }) => {
					calls.push(await request.json());
					current = [
						...current,
						{
							user_id: 'u3',
							email: 'new@example.com',
							role: 'member',
						},
					];
					return new HttpResponse(null, { status: 204 });
				},
			),
		);
		const user = userEvent.setup();

		renderWithProviders(<MembersScreen workspace={workspace} />);
		const dialog = await openAddMember(user);

		await user.type(within(dialog).getByLabelText('Email'), 'new@example.com');
		await user.click(within(dialog).getByLabelText('Role'));
		await user.click(screen.getByRole('option', { name: 'member' }));
		await user.click(
			within(dialog).getByRole('button', { name: 'Add member' }),
		);

		expect(await screen.findByText('new@example.com')).toBeInTheDocument();
		expect(calls).toEqual([{ email: 'new@example.com', role: 'member' }]);
	});

	test('a failed member load shows the problem code and retry action', async () => {
		server.use(
			http.get(`${ORIGIN}/api/v1/workspaces/${workspace.id}/members`, () =>
				problem('internal_error', 500, 'Could not load members'),
			),
		);

		renderWithProviders(<MembersScreen workspace={workspace} />);

		expect(await screen.findByText('Code: internal_error')).toBeInTheDocument();
		expect(
			screen.getByRole('button', { name: 'Try again' }),
		).toBeInTheDocument();
	});

	test('a 404 not_found shows No account with that email', async () => {
		server.use(
			membersHandler(),
			http.post(`${ORIGIN}/api/v1/workspaces/${workspace.id}/members`, () =>
				problem('not_found', 404, 'Request failed'),
			),
		);
		const user = userEvent.setup();

		renderWithProviders(<MembersScreen workspace={workspace} />);
		const dialog = await openAddMember(user);

		await user.type(
			within(dialog).getByLabelText('Email'),
			'missing@example.com',
		);
		await user.click(
			within(dialog).getByRole('button', { name: 'Add member' }),
		);

		expect(
			await screen.findByText('No account with that email'),
		).toBeInTheDocument();
	});
});
