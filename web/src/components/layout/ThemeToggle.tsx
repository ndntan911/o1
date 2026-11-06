import type { JSX } from 'react';
import { Monitor, Moon, Sun } from 'lucide-react';
import { useTheme } from 'next-themes';
import { Button } from '@/components/ui/button';
import { cn } from '@/lib/cn';

const options = [
	{ value: 'light', label: 'Light', Icon: Sun },
	{ value: 'dark', label: 'Dark', Icon: Moon },
	{ value: 'system', label: 'System', Icon: Monitor },
] as const;

export function ThemeToggle(): JSX.Element {
	const { theme = 'system', setTheme } = useTheme();

	return (
		<div
			role="group"
			aria-label="Theme"
			className="grid grid-cols-3 gap-1 rounded-lg border p-1"
		>
			{options.map(({ value, label, Icon }) => (
				<Button
					key={value}
					type="button"
					variant="ghost"
					size="sm"
					aria-pressed={theme === value}
					onClick={() => setTheme(value)}
					className={cn(
						'gap-1 px-1',
						theme === value && 'bg-muted text-foreground',
					)}
				>
					<Icon aria-hidden="true" />
					<span className="truncate">{label}</span>
				</Button>
			))}
		</div>
	);
}
