import { Link } from '@tanstack/react-router';
import { Button } from '@/components/ui/button';
import { useLogout } from '@/features/workspaces/hooks';
import { ThemeToggle } from './ThemeToggle';

export interface UserMenuProps {
	email: string;
}

export function UserMenu({ email }: UserMenuProps) {
	const logout = useLogout();

	return (
		<div className="space-y-3">
			<ThemeToggle />
			<p className="truncate text-sm text-muted-foreground" title={email}>
				{email}
			</p>
			<Link
				to="/account/security"
				className="flex h-8 w-full items-center rounded-lg px-2 text-sm text-muted-foreground hover:bg-muted hover:text-foreground [&[aria-current=page]]:text-foreground"
			>
				Security
			</Link>
			<Button
				type="button"
				variant="outline"
				size="sm"
				className="w-full"
				onClick={() => logout.mutate()}
				disabled={logout.isPending}
			>
				{logout.isPending ? 'Logging out…' : 'Log out'}
			</Button>
		</div>
	);
}
