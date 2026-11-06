import { describe, expect, test, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { ThemeProvider } from 'next-themes';
import { UserMenu } from './UserMenu';

vi.mock('@/features/workspaces/hooks', () => ({
	useLogout: () => ({ mutate: vi.fn(), isPending: false }),
}));

describe('UserMenu', () => {
	test('shows the account and the theme control', () => {
		render(
			<ThemeProvider
				attribute="class"
				defaultTheme="system"
				enableSystem
				storageKey="o1-theme"
			>
				<UserMenu email="owner@example.com" />
			</ThemeProvider>,
		);

		expect(screen.getByText('owner@example.com')).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Log out' })).toBeEnabled();
		expect(screen.getByRole('group', { name: 'Theme' })).toBeInTheDocument();
	});
});
