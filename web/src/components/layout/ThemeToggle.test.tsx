import { act } from 'react';
import { afterEach, describe, expect, test, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ThemeProvider } from 'next-themes';
import { ThemeToggle } from './ThemeToggle';

type ChangeListener = (event: MediaQueryListEvent) => void;

function mockSystemTheme(initialDark: boolean) {
	let dark = initialDark;
	const listeners = new Set<ChangeListener>();

	vi.spyOn(window, 'matchMedia').mockImplementation(
		(query: string) =>
			({
				get matches() {
					return dark;
				},
				media: query,
				onchange: null,
				addListener: (listener: ChangeListener) => listeners.add(listener),
				removeListener: (listener: ChangeListener) =>
					listeners.delete(listener),
				addEventListener: (_type: string, listener: ChangeListener) =>
					listeners.add(listener),
				removeEventListener: (_type: string, listener: ChangeListener) =>
					listeners.delete(listener),
				dispatchEvent: () => false,
			}) as unknown as MediaQueryList,
	);

	return {
		setSystemDark(next: boolean) {
			dark = next;
			listeners.forEach((listener) =>
				listener({ matches: next } as MediaQueryListEvent),
			);
		},
	};
}

function renderToggle() {
	return render(
		<ThemeProvider
			attribute="class"
			defaultTheme="system"
			enableSystem
			storageKey="o1-theme"
			disableTransitionOnChange
		>
			<ThemeToggle />
		</ThemeProvider>,
	);
}

afterEach(() => {
	localStorage.clear();
	document.documentElement.className = '';
	document.documentElement.style.colorScheme = '';
	vi.restoreAllMocks();
});

describe('ThemeToggle', () => {
	test('defaults to System and follows a dark OS preference', async () => {
		mockSystemTheme(true);
		renderToggle();

		expect(
			await screen.findByRole('button', { name: 'System' }),
		).toHaveAttribute('aria-pressed', 'true');
		await waitFor(() => expect(document.documentElement).toHaveClass('dark'));
	});

	test('choosing Dark applies the class and persists the choice', async () => {
		mockSystemTheme(false);
		const user = userEvent.setup();
		renderToggle();

		await user.click(screen.getByRole('button', { name: 'Dark' }));

		await waitFor(() => expect(document.documentElement).toHaveClass('dark'));
		expect(document.documentElement.style.colorScheme).toBe('dark');
		expect(localStorage.getItem('o1-theme')).toBe('dark');
		expect(screen.getByRole('button', { name: 'Dark' })).toHaveAttribute(
			'aria-pressed',
			'true',
		);
	});

	test('choosing Light removes the class and persists the choice', async () => {
		localStorage.setItem('o1-theme', 'dark');
		mockSystemTheme(false);
		const user = userEvent.setup();
		renderToggle();

		await waitFor(() => expect(document.documentElement).toHaveClass('dark'));
		await user.click(screen.getByRole('button', { name: 'Light' }));

		await waitFor(() =>
			expect(document.documentElement).not.toHaveClass('dark'),
		);
		expect(document.documentElement.style.colorScheme).toBe('light');
		expect(localStorage.getItem('o1-theme')).toBe('light');
	});

	test('System reacts when the OS preference changes', async () => {
		const system = mockSystemTheme(true);
		renderToggle();

		await waitFor(() => expect(document.documentElement).toHaveClass('dark'));

		await act(async () => {
			system.setSystemDark(false);
		});

		await waitFor(() =>
			expect(document.documentElement).not.toHaveClass('dark'),
		);
		// System is still the implicit default, so no override is stored.
		expect(localStorage.getItem('o1-theme')).toBeNull();
	});

	test('choosing System persists the preference', async () => {
		localStorage.setItem('o1-theme', 'dark');
		mockSystemTheme(false);
		const user = userEvent.setup();
		renderToggle();

		await waitFor(() => expect(document.documentElement).toHaveClass('dark'));
		await user.click(screen.getByRole('button', { name: 'System' }));

		await waitFor(() =>
			expect(document.documentElement).not.toHaveClass('dark'),
		);
		expect(screen.getByRole('button', { name: 'System' })).toHaveAttribute(
			'aria-pressed',
			'true',
		);
		expect(localStorage.getItem('o1-theme')).toBe('system');
	});
});
