<script lang="ts">
	import { TIER_NAMES, TIER_COLORS } from '$lib/shared/api/types';

	let { tier }: { tier: number } = $props();

	const colors = $derived(TIER_COLORS[tier] ?? { bg: 'bg-skeleton', text: 'text-foreground-muted' });
	const label = $derived(TIER_NAMES[tier] ?? `Tier ${tier}`);
</script>

<span class="inline-block rounded-full px-2 py-0.5 text-xs font-medium {colors.bg} {colors.text}">
	T{tier} {label}
</span>
