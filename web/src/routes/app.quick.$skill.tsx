import { createFileRoute, notFound, redirect } from '@tanstack/react-router';
import { Quick } from '../pages/app';

export const Route = createFileRoute('/app/quick/$skill')({
  beforeLoad: ({ params: { skill } }) => {
    if (skill === 'daily-observation' || skill === 'choice-quote')
      throw redirect({
        to: '/app/quick/$skill',
        params: { skill: 'stock_analysis' },
        replace: true,
      });
  },
  component: () => {
    const { skill } = Route.useParams();
    if (skill !== 'stock_analysis' && skill !== 'capital_flow' && skill !== 'deep_research')
      throw notFound();
    return <Quick key={skill} skill={skill} />;
  },
});
