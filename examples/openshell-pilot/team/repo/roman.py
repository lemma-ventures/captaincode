NUMERALS = [(1000, "M"), (500, "D"), (100, "C"), (50, "L"), (10, "X"), (5, "V"), (1, "I")]


def to_roman(number):
    result = ""
    for value, numeral in NUMERALS:
        while number >= value:
            result += numeral
            number -= value
    return result
