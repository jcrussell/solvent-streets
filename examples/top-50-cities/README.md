# top-50-cities

The 50 largest US cities by 2025 population. The idea comes from Ray
Delahanty's [CityNerd](https://www.youtube.com/@CityNerd) countdowns of
American urbanism; this example is **not affiliated with or endorsed by**
CityNerd or Ray Delahanty. The config's header comment gives the Census source
and how to run it.

## Cities, not metros

CityNerd usually ranks **metropolitan areas**. solvent-streets ranks
**cities**: one municipal boundary per entry. So places that are large metros
but smaller central cities, such as Boston and Atlanta, rank lower here than
they feel. That is the right unit for this project's question, because
pavement maintenance is paid for by the city that owns the streets, not by the
metro region.

## Why the biggest cities use coarser hexes

The grid is 150 m, and the geographically enormous jurisdictions override it
with 250–300 m hexes to keep compute time and database size manageable. Each
override is set per city in the config.
