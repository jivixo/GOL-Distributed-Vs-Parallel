import pandas as pd
import matplotlib.pyplot as plt

# Load CSV
df = pd.read_csv("jiviresults.csv")

# # Clean and convert CI column (remove % and cast to float)
# df["CI"] = df["CI"].astype(str).str.replace("%", "").astype(float)

# Clean and convert sec/op column (in case it’s also a string)
df["sec/op"] = df["sec/op"].astype(str).str.replace("   ,", "").astype(float)

# Drop 'geomean' row if it exists
df = df[df["Benchmark"] != "geomean"]

# Extract number of threads
df["Threads"] = df["Benchmark"].str.extract(r"-(\d+)-8").astype(int)

# Sort for nice plotting
df = df.sort_values("Threads")

# Plots the line graph
plt.figure(figsize=(8, 5))
plt.plot(df["Threads"], df["sec/op"], linestyle='-', marker='o', label="11th Gen Intel(R) Core(TM) i7-11370H")

plt.title("Game of Life Performance vs Threads (512x512x1000)")
plt.xlabel("Number of Threads")
plt.ylabel("sec/op")
plt.grid(True, linestyle="--", alpha=0.5)
plt.legend()
plt.tight_layout()
plt.show()