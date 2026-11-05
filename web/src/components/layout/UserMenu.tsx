import { Button } from '@/components/ui/button';
import { useLogout } from '@/features/workspaces/hooks';

export interface UserMenuProps {
	email: string;
}

export function UserMenu({ email }: UserMenuProps) {
	const logout = useLogout();

	return (
		<div className="space-y-2">
			<p className="truncate text-sm text-muted-foreground" title={email}>
				{email}
			</p>
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
