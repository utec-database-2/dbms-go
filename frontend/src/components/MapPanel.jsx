import { useEffect, useState } from "react";

import "leaflet/dist/leaflet.css";
import {
  MapContainer,
  TileLayer,
  Circle,
  CircleMarker,
  Popup,
  useMap,
} from "react-leaflet";
import { Map as MapIcon, Search } from "lucide-react";

import { searchSpatialRange } from "../api";

const DEFAULT_CENTER = [-12.0432, -77.0282];

function MapUpdater({ center }) {
  const map = useMap();

  useEffect(() => {
    map.setView(center, 13);

    // Recalcular el tamaño real del contenedor
    setTimeout(() => {
      map.invalidateSize();
    }, 100);
  }, [map, center]);

  useEffect(() => {
    const resizeObserver = new ResizeObserver(() => {
      map.invalidateSize();
    });

    resizeObserver.observe(map.getContainer());

    return () => {
      resizeObserver.disconnect();
    };
  }, [map]);

  return null;
}

function MapPanel() {
  const [latitude, setLatitude] = useState("-12.0432");
  const [longitude, setLongitude] = useState("-77.0282");
  const [radius, setRadius] = useState("5");
  const [metric, setMetric] = useState("haversine");

  const [spatialResult, setSpatialResult] = useState(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState(null);

  const handleMetricChange = (event) => {
    const value = event.target.value;

    setMetric(value);

    // Valores iniciales cómodos para cada métrica.
    if (value === "haversine") {
      setRadius("5");
    } else {
      // En Euclidiana trabajamos directamente con lat/lon.
      setRadius("0.05");
    }
  };

  const handleSearch = async () => {
    setError(null);

    const lat = Number(latitude);
    const lon = Number(longitude);
    const radiusValue = Number(radius);

    if (!Number.isFinite(lat) || !Number.isFinite(lon)) {
      setError("La latitud y longitud deben ser números válidos.");
      return;
    }

    if (!Number.isFinite(radiusValue) || radiusValue < 0) {
      setError("El radio debe ser un número mayor o igual a 0.");
      return;
    }

    setLoading(true);

    try {
      const data = await searchSpatialRange({
        latitude: lat,
        longitude: lon,
        radius: radiusValue,
        metric,
      });

      setSpatialResult(data);
    } catch (err) {
      setError(err.message);
      setSpatialResult(null);
    } finally {
      setLoading(false);
    }
  };

  const mapCenter = spatialResult?.center
    ? [spatialResult.center.lat, spatialResult.center.lon]
    : DEFAULT_CENTER;

  const results = spatialResult?.results || [];

  /*
   * Leaflet dibuja Circle en metros.
   *
   * Haversine:
   *   radius está en km → multiplicamos por 1000.
   *
   * Euclidiana:
   *   radius está en grados de coordenadas.
   *   Usamos una conversión aproximada para visualizarlo.
   */
  const circleRadiusMeters =
    metric === "haversine" ? Number(radius) * 1000 : Number(radius) * 111195;

  return (
    <section className="map-panel">
      <div className="map-panel-header">
        <div className="map-panel-title">
          <MapIcon size={19} />

          <div>
            <h2>Consulta espacial</h2>
            <p>Consulta por rango sobre el R-Tree</p>
          </div>
        </div>

        <div className="map-result-count">
          {spatialResult ? `${results.length} resultado(s)` : "Sin consulta"}
        </div>
      </div>

      <div className="spatial-controls">
        <div className="spatial-field">
          <label>Latitud</label>
          <input
            type="number"
            step="any"
            value={latitude}
            onChange={(e) => setLatitude(e.target.value)}
          />
        </div>

        <div className="spatial-field">
          <label>Longitud</label>
          <input
            type="number"
            step="any"
            value={longitude}
            onChange={(e) => setLongitude(e.target.value)}
          />
        </div>

        <div className="spatial-field">
          <label>Radio {metric === "haversine" ? "(km)" : "(grados)"}</label>

          <input
            type="number"
            min="0"
            step="any"
            value={radius}
            onChange={(e) => setRadius(e.target.value)}
          />
        </div>

        <div className="spatial-field">
          <label>Métrica</label>

          <select value={metric} onChange={handleMetricChange}>
            <option value="haversine">Haversine</option>
            <option value="euclidean">Euclidiana</option>
          </select>
        </div>

        <button
          className="spatial-search-button"
          onClick={handleSearch}
          disabled={loading}
        >
          <Search size={16} />
          {loading ? "Buscando..." : "Buscar"}
        </button>
      </div>

      {error && <div className="spatial-error">{error}</div>}

      <div className="spatial-map-wrapper">
        <MapContainer
          center={DEFAULT_CENTER}
          zoom={13}
          scrollWheelZoom={true}
          className="spatial-map"
        >
          <MapUpdater center={mapCenter} />

          <TileLayer
            attribution='&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a>'
            url="https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png"
          />

          {/* Centro de la consulta */}
          <CircleMarker
            center={mapCenter}
            radius={9}
            pathOptions={{
              color: "#2563eb",
              fillColor: "#2563eb",
              fillOpacity: 0.9,
            }}
          >
            <Popup>
              <strong>Punto de consulta</strong>
              <br />
              Latitud: {mapCenter[0].toFixed(6)}
              <br />
              Longitud: {mapCenter[1].toFixed(6)}
            </Popup>
          </CircleMarker>

          {/* Radio de búsqueda */}
          {spatialResult && (
            <Circle
              center={mapCenter}
              radius={circleRadiusMeters}
              pathOptions={{
                color: "#2563eb",
                fillColor: "#2563eb",
                fillOpacity: 0.08,
              }}
            />
          )}

          {/* Resultados */}
          {results.map((point) => (
            <CircleMarker
              key={point.id}
              center={[point.lat, point.lon]}
              radius={7}
              pathOptions={{
                color: "#dc2626",
                fillColor: "#dc2626",
                fillOpacity: 0.8,
              }}
            >
              <Popup>
                <strong>{point.name}</strong>
                <br />
                Latitud: {point.lat.toFixed(6)}
                <br />
                Longitud: {point.lon.toFixed(6)}
                <br />
                Distancia:{" "}
                {metric === "haversine"
                  ? `${point.distance.toFixed(2)} km`
                  : point.distance.toFixed(6)}
              </Popup>
            </CircleMarker>
          ))}
        </MapContainer>
      </div>
    </section>
  );
}

export default MapPanel;
