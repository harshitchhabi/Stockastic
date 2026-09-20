import { Leaderboard } from "../Leaderboard";

/** Organisers always see the standings, whatever teams are allowed to see. */
export function AdminStandings() {
  return (
    <div className="page">
      <div className="page-head">
        <h1>Standings</h1>
        <span className="dim">Refreshed on the rulebook interval. Teams cannot see this page.</span>
      </div>
      <Leaderboard />
    </div>
  );
}
