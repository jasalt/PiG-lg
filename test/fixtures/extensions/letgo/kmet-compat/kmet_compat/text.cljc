(ns kmet-compat.text
  "Pure logic shared by both hosts. No host API."
  (:require [clojure.string :as str]))

(defn describe [text]
  (let [words (str/split (str/trim text) #"\s+")
        longest (reduce (fn [a b] (if (> (count b) (count a)) b a)) "" words)]
    (str (count words) " words, longest " longest)))
