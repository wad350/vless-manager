import { Navigate, Route, Routes } from "react-router-dom";
import { Layout } from "./components/Layout";
import { useAuth } from "./auth/AuthContext";
import { LoginPage } from "./pages/LoginPage";
import { DashboardPage } from "./pages/DashboardPage";
import { SquadsPage } from "./pages/SquadsPage";
import { NodesPage } from "./pages/NodesPage";
import { UsersPage } from "./pages/UsersPage";
import { BandwidthLimitersPage } from "./pages/BandwidthLimitersPage";
import { TrafficLimitersPage } from "./pages/TrafficLimitersPage";
import { ConnectionLimitersPage } from "./pages/ConnectionLimitersPage";
import { RateLimitersPage } from "./pages/RateLimitersPage";

export function App() {
  const { auth } = useAuth();
  if (!auth) {
    return (
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="*" element={<Navigate to="/login" replace />} />
      </Routes>
    );
  }
  return (
    <Layout>
      <Routes>
        <Route path="/" element={<DashboardPage />} />
        <Route path="/squads" element={<SquadsPage />} />
        <Route path="/nodes" element={<NodesPage />} />
        <Route path="/users" element={<UsersPage />} />
        <Route path="/bandwidth-limiters" element={<BandwidthLimitersPage />} />
        <Route path="/traffic-limiters" element={<TrafficLimitersPage />} />
        <Route path="/connection-limiters" element={<ConnectionLimitersPage />} />
        <Route path="/rate-limiters" element={<RateLimitersPage />} />
        <Route path="/login" element={<Navigate to="/" replace />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </Layout>
  );
}
